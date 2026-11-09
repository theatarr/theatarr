#!/usr/bin/env python3
"""Extract Radarr's ParserTests cases to JSON.

Usage:
    python3 -I extract.py <radarr clone> <out dir>

Reads src/NzbDrone.Core.Test/ParserTests/*.cs from the clone (read-only) and writes one
<Fixture>.json per fixture into <out dir>. The output is deterministic, so the files can be
regenerated and diffed. Cases already marked known_failure in the existing output keep their
reason (matched by id), so regenerating never drops the bookkeeping.

Schema (shared with the Sonarr port):
    {"source": "...", "fixture": "...", "cases": [
        {"id": "Fixture/method/n", "line": 57, "call": "parseQuality",
         "input": "...",                 # or "inputs": [...] for a test that loops over inputs
         "args": {...},                  # optional extra arguments of the call
         "expect": {"dot.path": value},  # "" is the whole value; null means null/absent
         "contains": {"dot.path": [..]}, # optional: every listed value occurs in that list
         "compare": "cleanMovieTitle",   # optional: compare both sides after this function
         "known_failure": "reason"}]}    # optional

Dot paths: object keys and array indices (languages.0.name); "#" is an array's length
(languages.#), and "#.key" projects a key over an array (languages.#.name).
"""

import hashlib
import json
import os
import random
import re
import sys

COMMIT = "a96bf7c"
TESTS = "src/NzbDrone.Core.Test/ParserTests"


# ---------------------------------------------------------------------------------------------
# A small C# lexer: enough for attribute arguments and static case tables.

class Tok:
    __slots__ = ("kind", "val", "line")

    def __init__(self, kind, val, line):
        self.kind, self.val, self.line = kind, val, line

    def __repr__(self):
        return "%s:%r@%d" % (self.kind, self.val, self.line)


ESCAPES = {"n": "\n", "t": "\t", "r": "\r", "0": "\0", "\\": "\\", "\"": "\"", "'": "'", "a": "\a",
           "b": "\b", "f": "\f", "v": "\v"}


def lex(src):
    toks, i, line, n = [], 0, 1, len(src)
    while i < n:
        c = src[i]
        if c == "\n":
            line += 1
            i += 1
        elif c.isspace() or c == "\ufeff":
            i += 1
        elif src.startswith("//", i):
            while i < n and src[i] != "\n":
                i += 1
        elif src.startswith("/*", i):
            j = src.index("*/", i + 2)
            line += src.count("\n", i, j)
            i = j + 2
        elif c == "@" and i + 1 < n and src[i + 1] == "\"":
            i += 2
            buf, start = [], line
            while True:
                if src[i] == "\"":
                    if i + 1 < n and src[i + 1] == "\"":
                        buf.append("\"")
                        i += 2
                        continue
                    i += 1
                    break
                if src[i] == "\n":
                    line += 1
                buf.append(src[i])
                i += 1
            toks.append(Tok("str", "".join(buf), start))
        elif c == "\"" or c == "'":
            q = c
            i += 1
            buf = []
            while src[i] != q:
                if src[i] == "\\":
                    e = src[i + 1]
                    if e == "u":
                        buf.append(chr(int(src[i + 2:i + 6], 16)))
                        i += 6
                        continue
                    if e == "x":
                        j = i + 2
                        while j < n and j < i + 6 and src[j] in "0123456789abcdefABCDEF":
                            j += 1
                        buf.append(chr(int(src[i + 2:j], 16)))
                        i = j
                        continue
                    buf.append(ESCAPES[e])
                    i += 2
                    continue
                buf.append(src[i])
                i += 1
            i += 1
            toks.append(Tok("str" if q == "\"" else "char", "".join(buf), line))
        elif c.isdigit():
            j = i
            while j < n and (src[j].isalnum() or src[j] == "."):
                j += 1
            text = src[i:j].rstrip("LlUuFfDdMm")
            toks.append(Tok("num", float(text) if "." in text else int(text), line))
            i = j
        elif c.isalpha() or c == "_":
            j = i
            while j < n and (src[j].isalnum() or src[j] == "_"):
                j += 1
            toks.append(Tok("id", src[i:j], line))
            i = j
        else:
            toks.append(Tok("p", c, line))
            i += 1
    return toks


class Enum:
    """A reference such as QualitySource.TV or Language.English."""

    def __init__(self, name):
        self.name = name

    def __repr__(self):
        return self.name


class Named:
    def __init__(self, name, value):
        self.name, self.value = name, value


def parse_value(toks, i):
    t = toks[i]
    if t.kind in ("str", "char", "num"):
        v, i = t.val, i + 1
    elif t.kind == "p" and t.val == "-" and toks[i + 1].kind == "num":
        v, i = -toks[i + 1].val, i + 2
    elif t.kind == "id" and t.val in ("true", "false"):
        v, i = t.val == "true", i + 1
    elif t.kind == "id" and t.val == "null":
        v, i = None, i + 1
    elif t.kind == "id" and t.val == "new":
        i += 1
        while toks[i].kind == "id":  # element type
            i += 1
        assert toks[i].val == "[", toks[i]
        if toks[i + 1].kind == "num":  # new string[0]
            assert toks[i + 1].val == 0 and toks[i + 2].val == "]"
            return [], i + 3
        assert toks[i + 1].val == "]", toks[i + 1]
        i += 2
        v, i = parse_list(toks, i, "{", "}")
    elif t.kind == "id" and t.val == "nameof":
        return toks[i + 2].val, i + 4
    elif t.kind == "id" and toks[i + 1].val == "=" and toks[i + 2].val != "=":
        v, i = parse_value(toks, i + 2)
        return Named(t.val, v), i
    elif t.kind == "id":
        parts = [t.val]
        i += 1
        while toks[i].val == "." and toks[i + 1].kind == "id":
            parts.append(toks[i + 1].val)
            i += 2
        v = Enum(".".join(parts))
    else:
        raise ValueError("unexpected token %r" % t)
    # Postfix calls on literals, e.g. @"C:\Test\..".AsOsAgnostic()
    while toks[i].val == "." and toks[i + 1].kind == "id" and toks[i + 2].val == "(":
        name = toks[i + 1].val
        assert toks[i + 3].val == ")", "only argument-less postfix calls are supported"
        v = POSTFIX[name](v)
        i += 4
    return v, i


def parse_list(toks, i, open_, close):
    assert toks[i].val == open_, toks[i]
    i += 1
    out = []
    while toks[i].val != close:
        v, i = parse_value(toks, i)
        out.append(v)
        if toks[i].val == ",":
            i += 1
    return out, i + 1


def as_os_agnostic(path):
    # NzbDrone.Test.Common StringExtensions.AsOsAgnostic on a non-Windows host (Radarr runs on Linux).
    if len(path) > 2 and path[1] == ":":
        path = "/" + path.replace(":", "")
    return path.replace("\\", "/")


POSTFIX = {"AsOsAgnostic": as_os_agnostic}


class Method:
    def __init__(self, name, line, params, attrs):
        self.name, self.line, self.params, self.attrs = name, line, params, attrs


def scan(toks):
    """Returns (methods, tables). Each method carries its [TestCase]/[Test]/[TestCaseSource] attributes."""
    methods, tables, pending, i = [], {}, [], 0
    while i < len(toks):
        t = toks[i]
        if t.val == "[" and toks[i + 1].kind == "id" and toks[i + 1].val in ("TestCase", "Test", "TestCaseSource"):
            kind = toks[i + 1].val
            line = toks[i + 1].line
            i += 2
            args = []
            if toks[i].val == "(":
                args, i = parse_list(toks, i, "(", ")")
            assert toks[i].val == "]", toks[i]
            i += 1
            pending.append((kind, line, args))
            continue
        if t.kind == "id" and t.val == "public" and toks[i + 1].val == "void":
            name = toks[i + 2].val
            j = i + 4
            params, depth, cur = [], 0, []
            while not (toks[j].val == ")" and depth == 0):
                if toks[j].val in ("(", "["):
                    depth += 1
                elif toks[j].val in (")", "]"):
                    depth -= 1
                if toks[j].val == "," and depth == 0:
                    params.append(cur)
                    cur = []
                else:
                    cur.append(toks[j].val)
                j += 1
            if cur:
                params.append(cur)
            methods.append(Method(name, toks[i + 2].line, [p[-1] for p in params if p], pending))
            pending = []
            i = j
            continue
        if t.kind == "id" and t.val == "object" and toks[i + 1].val == "[" and toks[i + 2].val == "]" \
                and toks[i + 3].kind == "id" and toks[i + 4].val == "=":
            name = toks[i + 3].val
            rows, i = parse_list(toks, i + 5, "{", "}")
            tables[name] = rows
            continue
        i += 1
    return methods, tables


# ---------------------------------------------------------------------------------------------
# Enum tables, read from the Radarr sources so names and ids never drift.

def read_enums(root):
    langs = {}
    src = open(os.path.join(root, "src/NzbDrone.Core/Languages/Language.cs"), encoding="utf-8-sig").read()
    for m in re.finditer(r'public static Language (\w+) => new Language\((-?\d+), "([^"]+)"\)', src):
        langs[m.group(1)] = m.group(3)
    quals = {}
    src = open(os.path.join(root, "src/NzbDrone.Core/Qualities/Quality.cs"), encoding="utf-8-sig").read()
    for m in re.finditer(r'public static Quality (\w+) => new Quality\((\d+), "([^"]+)"', src):
        quals[m.group(1)] = m.group(3)
    return langs, quals


LANGS, QUALS = {}, {}


def enum(v):
    """The arr JSON value of an enum reference."""
    assert isinstance(v, Enum), v
    typ, _, member = v.name.partition(".")
    if typ in ("QualitySource", "Modifier"):
        return member.lower()
    if typ == "Resolution":
        return 0 if member == "Unknown" else int(member[1:-1])
    if typ == "Language":
        return LANGS[member]
    if typ == "Quality":
        return QUALS[member]
    if typ == "QualityDetectionSource":
        return member[0].lower() + member[1:]
    raise ValueError(v.name)


# ---------------------------------------------------------------------------------------------
# Per-method handlers: map the C# assertions to (call, input, expect). Each returns a dict, or a
# list of dicts for a test whose body calls the parser more than once with different expectations.

def case(call, inp, expect=None, **kw):
    c = {"call": call}
    if isinstance(inp, list):
        c["inputs"] = inp
    else:
        c["input"] = inp
    for k in ("args",):
        if k in kw:
            c[k] = kw.pop(k)
    c["expect"] = expect or {}
    c.update(kw)
    return c


def verify_quality(title, source, proper, resolution, modifier="none"):
    # QualityParserFixture.ParseAndVerifyQuality
    e = {}
    if resolution != 0:
        e["quality.resolution"] = resolution
    e["quality.source"] = source
    if modifier != "none":
        e["quality.modifier"] = modifier
    e["revision.version"] = 2 if proper else 1
    return case("parseQuality", title, e)


QUALITY_METHODS = {
    "should_parse_ts": ("telesync", 720, "none"),
    "should_parse_cam": ("cam", 0, "none"),
    "should_parse_sdtv_quality": ("tv", 480, "none"),
    "should_parse_dvd_quality": ("dvd", 0, "none"),
    "should_parse_dvdr_quality": ("dvd", 480, "remux"),
    "should_parse_webdl480p_quality": ("webdl", 480, "none"),
    "should_parse_webrip480p_quality": ("webrip", 480, "none"),
    "should_parse_bluray480p_quality": ("bluray", 480, "none"),
    "should_parse_hdtv720p_quality": ("tv", 720, "none"),
    "should_parse_hdtv1080p_quality": ("tv", 1080, "none"),
    "should_parse_hdtv2160p_quality": ("tv", 2160, "none"),
    "should_parse_webdl720p_quality": ("webdl", 720, "none"),
    "should_parse_webrip720p_quality": ("webrip", 720, "none"),
    "should_parse_webdl1080p_quality": ("webdl", 1080, "none"),
    "should_parse_webrip1080p_quality": ("webrip", 1080, "none"),
    "should_parse_webdl2160p_quality": ("webdl", 2160, "none"),
    "should_parse_webrip2160p_quality": ("webrip", 2160, "none"),
    "should_parse_bluray720p_quality": ("bluray", 720, "none"),
    "should_parse_bluray1080p_quality": ("bluray", 1080, "none"),
    "should_parse_bluray2160p_quality": ("bluray", 2160, "none"),
    "should_parse_bluray576p_quality": ("bluray", 576, "none"),
    "should_parse_remux720p_as_bluray720p_quality": ("bluray", 720, "none"),
    "should_parse_remux1080p_quality": ("bluray", 1080, "remux"),
    "should_parse_remux2160p_quality": ("bluray", 2160, "remux"),
    "should_parse_brdisk_1080p_quality": ("bluray", 1080, "brdisk"),
    "should_parse_raw_quality": ("tv", 1080, "rawhd"),
    "quality_parse": ("unknown", 0, "none"),
}

DETECTION_METHODS = {
    "should_parse_full_quality_from_name": ("name", "name"),
    "should_parse_resolution_from_name": ("unknown", "name"),
    "should_parse_source_from_name": ("name", "unknown"),
    "should_parse_quality_from_extension": ("extension", "extension"),
    "should_parse_resolution_from_name_and_source_from_extension": ("extension", "name"),
}


def h_quality(m, a, tables):
    if m in QUALITY_METHODS:
        src, res, mod = QUALITY_METHODS[m]
        proper = a[1] if len(a) > 1 else False
        return verify_quality(a[0], src, proper, res, mod)
    if m in DETECTION_METHODS:
        s, r = DETECTION_METHODS[m]
        return case("parseQualityDetection", a[0], {"sourceDetectionSource": s, "resolutionDetectionSource": r})
    if m == "should_be_able_to_parse_repack":
        return case("parseQuality", a[0], {"revision.version": a[2], "revision.isRepack": a[1]})
    if m == "should_parse_quality_from_other_source":
        q, src, res, mod = a[0], enum(a[1]), enum(a[2]), enum(a[3]) if len(a) > 3 else "none"
        titles = ["My series S01E01 " + q.replace(" ", c) for c in "-. _"]
        v = verify_quality(titles, src, False, res, mod)
        return v
    return None


def h_anime_version(m, a, tables):
    return case("parseQuality", a[0], {"revision.version": a[1]})


def h_extended(m, a, tables):
    if m == "should_parse_reality_from_title":
        return case("parseQuality", a[0], {"revision.real": a[1]})
    if m == "should_parse_version_from_title":
        return case("parseQuality", a[0], {"revision.version": a[1]})
    if m == "should_parse_ultrahd_from_title":
        return case("parseQuality", a[0], {"quality.resolution": 2160})
    return None


def lang_contains(*names):
    return {"languages.#.name": list(names)}


def h_language(m, a, tables):
    t = a[0]
    if m in ("should_parse_subtitle_language_english", "should_parse_subtitle_language"):
        return case("parseSubtitleLanguage", t, {"name": "English"})
    if m == "should_parse_subtitle_language_unknown":
        return case("parseSubtitleLanguage", t, {"name": "Unknown"})
    if m == "should_parse_language_french_english":
        return case("parseMovieTitle", t, args={"isDir": True}, contains=lang_contains("French", "English"))
    if m == "should_parse_language_spanish_latino":
        return case("parseLanguages", t, {"0.id": 37})
    if m == "should_parse_language_polish_subbed":
        return case("parseMovieTitle", t, args={"isDir": True}, contains=lang_contains("Unknown"))
    if m == "should_add_original_language_to_german_release_with_dl_tag":
        return case("parseMovieTitle", t, {"languages.#": 2}, contains=lang_contains("German", "Original"))
    if m == "should_not_add_original_language_to_german_release_when_title_contains_web_dl":
        return case("parseMovieTitle", t, {"languages.#": 1}, contains=lang_contains("German"))
    if m == "should_parse_original_title_from_release_name":
        return case("parseMovieTitle", t, {"languages.#": 1}, contains=lang_contains("Original"))
    if m == "should_add_original_language_and_english_to_german_release_with_ml_tag":
        return case("parseMovieTitle", t, {"languages.#": 3},
                    contains=lang_contains("German", "Original", "English"))
    if m == "should_parse_title_and_tags":
        # BeEquivalentTo: same tags in any order; string equivalence is case-insensitive in
        # FluentAssertions, encoded here as exact (the cases use lower-case titles).
        e = {"languageTags.#": len(a[1]), "title": a[2], "language.name": LANG_BY_NAME[a[3].lower()]}
        c = case("parseSubtitleLanguageInformation", t, e)
        if a[1]:
            c["contains"] = {"languageTags": a[1]}
        return c
    if m == "should_not_parse_false_title":
        return case("parseSubtitleLanguageInformation", t,
                    {"language.name": "Unknown", "languageTags.#": 0, "rawTitle": None})
    mm = re.match(r"should_parse_language_(\w+)$", m)
    if mm:
        key = mm.group(1)
        name = LANG_METHOD.get(key)
        if name is None:
            name = LANG_BY_NAME[key.replace("_", " ")]
        body = METHOD_BODIES[m]
        if "LanguageParser.ParseLanguages" in body:
            return case("parseLanguages", t, contains={"#.name": [name]})
        is_dir = "ParseMovieTitle(postTitle, true)" in body
        return case("parseMovieTitle", t, args={"isDir": True} if is_dir else None,
                    contains=lang_contains(name))
    return None


LANG_METHOD = {"brazilian_portuguese": "Portuguese (Brazil)", "ukrainian_multi": "Ukrainian"}
LANG_BY_NAME = {}
METHOD_BODIES = {}


def h_iso(m, a, tables):
    if m in ("unknown_or_invalid_code_should_return_null", "should_not_return_portuguese"):
        return case("findIsoLanguage", a[0], {"": None})
    mm = re.match(r"should_return_(?:iso_language_for_)?(\w+)$", m)
    return case("findIsoLanguage", a[0], {"language.name": LANG_BY_NAME[mm.group(1).lower()]})


def h_release_group(m, a, tables):
    if m == "should_not_parse_anime_hash_as_release_group":
        return case("parseReleaseGroup", a[0], {"": None})
    return case("parseReleaseGroup", a[0], {"": a[1]})


def h_url(m, a, tables):
    if m == "should_not_parse_url_in_name":
        return case("parseMovieTitle", a[0], {"movieTitle": a[1]}, compare="cleanMovieTitle")
    return case("parseReleaseGroup", a[0], {"": a[1]})


def h_edition(m, a, tables):
    if m == "should_parse_edition":
        return case("parseMovieTitle", a[0], {"edition": a[1]})
    return case("parseMovieTitle", a[0], {"edition": ""})


def h_normalize(m, a, tables):
    fmts = {
        "should_remove_common_words_from_middle_of_title": (["word.{0}.word", "word {0} word", "word-{0}-word"],
                                                            lambda w: "wordword"),
        "should_not_remove_common_words_from_end_of_title": (["word.word.{0}", "word-word-{0}", "word-word {0}"],
                                                             lambda w: "wordword" + w.lower()),
        "should_remove_a_from_middle_of_title": (["word.{0}.word", "word {0} word", "word-{0}-word"],
                                                 lambda w: "wordword"),
        "should_not_remove_a_when_at_start_of_acronym": (["word.{0}.N.K.L.E.word", "word {0} N K L E word",
                                                          "word-{0}-N-K-L-E-word"], lambda w: "wordankleword"),
        "should_not_remove_a_when_at_end_of_acronym": (["word.N.K.L.E.{0}.word", "word N K L E {0} word",
                                                        "word-N-K-L-E-{0}-word"], lambda w: "wordnkleaword"),
        "should_not_remove_common_words_in_the_middle_of_word": (["word.{0}word", "word {0}word", "word-{0}word",
                                                                  "word{0}.word", "word{0}-word", "word{0}-word"],
                                                                 lambda w: "word" + w.lower() + "word"),
        "should_not_clean_word_from_beginning_of_string": (["{0}.word.word", "{0}-word-word", "{0} word word"],
                                                           lambda w: w + "wordword"),
    }
    if m in fmts:
        f, exp = fmts[m]
        w = a[0] if a else "a"
        return case("cleanMovieTitle", [s.format(w) for s in f], {"": exp(w)})
    if m == "should_not_clean_trailing_a":
        return case("cleanMovieTitle", "Tokyo Ghoul A", {"": "tokyoghoula"})
    return case("cleanMovieTitle", a[0], {"": a[1]})


def md5_chain():
    out, h = [], "CRAPPY TEST SEED"
    for _ in range(100):
        h = hashlib.md5(h.encode("utf-8")).hexdigest().upper()
        out.append(h)
    return out


def random_titles(length, n=500):
    charset = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
    rnd = random.Random(length)
    return ["".join(rnd.choice(charset) for _ in range(length)) for _ in range(n)]


def h_crap(m, a, tables):
    if m == "should_not_parse_md5":
        return case("parseMovieTitle", md5_chain(), {"": None})
    if m == "should_not_parse_random":
        # The C# test draws 500 titles from System.Random; these are 500 drawn with a fixed seed.
        return case("parseMovieTitle", random_titles(a[0]), {"": None})
    return case("parseMovieTitle", a[0], {"": None})


def h_hashed(m, a, tables):
    path, title, quality, group = a
    return case("parseMoviePath", path,
                {"primaryMovieTitle": title, "quality.quality.name": enum(quality), "releaseGroup": group})


def h_scene(m, a, tables):
    if m == "should_return_true_for_scene_names":
        return case("isSceneTitle", a[0], {"": True})
    if m == "should_return_false_for_non_scene_names":
        return case("isSceneTitle", a[0], {"": False})
    return case("getSceneTitle", a[0], {"": a[1]})


def h_slug(m, a, tables):
    fixed = {
        "should_replace_special_characters": ({}, "testresult"),
        "should_lowercase_capitals": ({}, "this-is-a-mixed-case-sensitive-value"),
        "should_trim_trailing_dashes_and_underscores": ({}, "test"),
        "should_replace_spaces_with_dash": ({}, "test-result"),
        "should_replace_special_characters_with_dash_when_enabled": ({"invalidDashReplacement": True}, "test-result"),
        "should__not_replace_special_characters_with_dash_when_disabled": ({"invalidDashReplacement": False},
                                                                           "testresult"),
    }
    if m in fixed:
        args, exp = fixed[m]
        return case("toUrlSlug", a[0], {"": exp}, args=args or None)
    if m in ("should_replace_accents", "should_replace_double_occurence"):
        return case("toUrlSlug", a[0], {"": a[1]})
    if m == "should_trim_trailing_dashes_and_underscores_based_on_list":
        return case("toUrlSlug", a[0], {"": a[2]},
                    args={"invalidDashReplacement": False, "trimEndChars": a[1], "deduplicateChars": ""})
    if m == "should_replace_duplicate_characters_based_on_list":
        return case("toUrlSlug", a[0], {"": a[2]},
                    args={"invalidDashReplacement": False, "trimEndChars": "", "deduplicateChars": a[1]})
    nulls = {
        "should_handle_null_trim_parameters": (None, "-_"),
        "should_handle_null_dedupe_parameters": ("-_", None),
        "should_handle_empty_trim_parameters": ("", "-_"),
        "should_handle_empty_dedupe_parameters": ("-_", ""),
    }
    if m in nulls:
        t, d = nulls[m]
        return case("toUrlSlug", "test", {"": "test"},
                    args={"invalidDashReplacement": False, "trimEndChars": t, "deduplicateChars": d})
    return None


def h_parser(m, a, tables):
    t = a[0] if a else None
    if m == "should_remove_accents_from_title":
        return case("cleanMovieTitle", "Carniv\u00e0le", {"": "carnivale"})
    if m == "should_parse_movie_title":
        return case("parseMovieTitle", t, {"primaryMovieTitle": a[1]})
    if m == "should_parse_anime_movie_title":
        return case("parseMovieTitle", t, {"primaryMovieTitle": a[1], "releaseGroup": a[2], "year": a[3]})
    if m == "should_parse_anime_movie_title_without_year":
        return case("parseMovieTitle", t, {"primaryMovieTitle": a[1], "releaseGroup": a[2]})
    if m == "should_parse_german_movie":
        return case("parseMovieTitle", t, {"primaryMovieTitle": a[1], "edition": a[2], "year": a[3]})
    if m == "should_parse_movie_alternative_titles":
        e = {"movieTitles.#": len(a[1])}
        for i, x in enumerate(a[1]):
            e["movieTitles.%d" % i] = x
        return case("parseMovieTitle", t, e, args={"isDir": True})
    if m == "should_parse_movie_folder_name":
        return case("parseMovieTitle", t, {"primaryMovieTitle": a[1]}, args={"isDir": True})
    if m == "should_parse_movie_year":
        return case("parseMovieTitle", t, {"year": a[1]})
    if m == "should_parse_tmdb_id":
        return case("parseMovieTitle", t, {"tmdbId": a[1]})
    if m == "should_not_parse_wrong_language_in_title":
        return case("parseMovieTitle", t, {"languages.#": 1, "languages.0.name": "Unknown"}, args={"isDir": True})
    if m == "should_parse_hardcoded_subs":
        return case("parseMovieTitle", t, {"hardcodedSubs": a[1]})
    if m == "should_parse_imdb_in_title":
        return case("parseMovieTitle", t, {"imdbId": a[1]}, args={"isDir": True})
    if m == "should_normalize_imdbid":
        return case("normalizeImdbId", t, {"": a[1]})
    return None


def h_roman(m, a, tables):
    # [Range(1, 20)] over a dictionary file: one case per value.
    table = ROMAN
    if m == "should_convert_arabic_numeral_to_roman_numeral":
        return [case("arabicToRoman", str(k), {"": table[k]}) for k in range(1, 21)]
    if m == "should_convert_roman_numeral_to_arabic_numeral":
        return [case("romanToArabic", table[k], {"": k}) for k in range(1, 21)]
    return None


ROMAN = {}

FIXTURES = [
    ("QualityParserFixture", h_quality),
    ("AnimeVersionFixture", h_anime_version),
    ("ExtendedQualityParserRegex", h_extended),
    ("LanguageParserFixture", h_language),
    ("IsoLanguagesFixture", h_iso),
    ("ReleaseGroupParserFixture", h_release_group),
    ("ParserFixture", h_parser),
    ("EditionParserFixture", h_edition),
    ("NormalizeTitleFixture", h_normalize),
    ("CrapParserFixture", h_crap),
    ("HashedReleaseFixture", h_hashed),
    ("SceneCheckerFixture", h_scene),
    ("SlugParserFixture", h_slug),
    ("UrlFixture", h_url),
    ("RomanNumeralTests/RomanNumeralConversionFixture", h_roman),
]


def method_bodies(src):
    out = {}
    for m in re.finditer(r"public void (\w+)\([^)]*\)\s*\{", src):
        depth, i = 1, m.end()
        while depth:
            depth += {"{": 1, "}": -1}.get(src[i], 0)
            i += 1
        out[m.group(1)] = src[m.end():i]
    return out


def main():
    root, out = sys.argv[1], sys.argv[2]
    langs, quals = read_enums(root)
    LANGS.update(langs)
    QUALS.update(quals)
    for name in langs.values():
        LANG_BY_NAME[name.lower()] = name
    ROMAN.update({int(k): v for k, v in json.load(
        open(os.path.join(root, "src/NzbDrone.Core.Test/Files/ArabicRomanNumeralDictionary.JSON"),
             encoding="utf-8-sig")).items()})
    total = 0
    for rel, handler in FIXTURES:
        path = os.path.join(root, TESTS, rel + ".cs")
        src = open(path, encoding="utf-8-sig").read()
        METHOD_BODIES.clear()
        METHOD_BODIES.update(method_bodies(src))
        methods, tables = scan(lex(src))
        fixture = rel.split("/")[-1]
        dest = os.path.join(out, fixture + ".json")
        old = {}
        if os.path.exists(dest):
            for c in json.load(open(dest, encoding="utf-8"))["cases"]:
                if "known_failure" in c:
                    old[c["id"]] = c["known_failure"]
        cases = []
        for meth in methods:
            rows = []
            for kind, line, args in meth.attrs:
                if kind == "TestCase":
                    rows.append((line, [x for x in args if not isinstance(x, Named)]))
                elif kind == "TestCaseSource":
                    src_name = args[0]
                    for r in tables[src_name]:
                        rows.append((line, r))
            if not rows and any(k == "Test" for k, _, _ in meth.attrs):
                rows.append((meth.line, []))
            for n, (line, args) in enumerate(rows):
                got = handler(meth.name, args, tables)
                if got is None:
                    raise SystemExit("%s: no handler for %s" % (fixture, meth.name))
                subs = got if isinstance(got, list) else [got]
                for k, c in enumerate(subs):
                    cid = "%s/%s/%d" % (fixture, meth.name, n)
                    if len(subs) > 1:
                        cid += "/%d" % k
                    c = dict({"id": cid, "line": line}, **c)
                    if c.get("args") is None:
                        c.pop("args", None)
                    if cid in old:
                        c["known_failure"] = old[cid]
                    cases.append(c)
        doc = {"source": "Radarr src/NzbDrone.Core.Test/ParserTests/%s.cs @ %s" % (rel, COMMIT),
               "fixture": fixture, "cases": cases}
        with open(dest, "w", encoding="utf-8") as f:
            json.dump(doc, f, ensure_ascii=False, indent=1)
            f.write("\n")
        total += len(cases)
        print("%-40s %5d" % (fixture, len(cases)))
    print("%-40s %5d" % ("total", total))


if __name__ == "__main__":
    main()
