#!/usr/bin/env python3
"""Extract Sonarr's ParserTests cases into testdata/parsertests/<Fixture>.json.

Usage: python3 extract.py [--v5] SONARR_SRC_ROOT   (a Sonarr source checkout, develop @ cab419a)

With --v5 it reads Sonarr v5 (a v5 checkout, develop @ a6171f0ca) and writes testdata/parsertests_v5/,
which TestParserTestsV5 runs against the V5 parser. v5 asserts SeasonNumbers where v4 asserted SeasonNumber: a
"seasonNumber": n expectation becomes "seasonNumbers": [n].

Each [TestCase] line becomes one case; a [Test] method or one TestCaseSource item becomes one case.

The case schema is the one the Radarr parser tests use (internal/decide/parser/radarr), so one matcher runs both sets:

    input / inputs  the title, or a list of titles that must each meet the expectations (C# loops)
    args            extra call arguments (object-valued inputs, template parameters)
    expect          {path: value}; null also matches a field the arr JSON leaves out
    contains        {path: [values]}: each value occurs in the list at path
    compare         compare both sides after this function (cleanSeriesTitle)
    not             {path: value}: the value at path is not this (Sonarr extension: NotBeNull, NotBe)

Paths are dot paths; "" is the whole value, "#" an array's length and "#.key" the key across all elements.
The handlers below write expectations with a few "|op" suffixes (|count, |contains, |anyOrder, |not,
|cleanEquals); to_schema() turns them into the fields above.

known_failure reasons are kept in known_failures.json (id -> reason) and merged in, so the data can be regenerated.
"""

import hashlib
import json
import os
import random
import re
import sys

ARGS = [a for a in sys.argv[1:] if a != "--v5"]
V5 = "--v5" in sys.argv[1:]
if not ARGS:
    sys.exit(__doc__)
SRC = ARGS[0]
COMMIT = "a6171f0ca" if V5 else "cab419a"
TESTS = os.path.join(SRC, "src/NzbDrone.Core.Test/ParserTests")
HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, "parsertests_v5" if V5 else "parsertests")
KNOWN = "known_failures_v5.json" if V5 else "known_failures.json"

QUALITY_NAMES = {
    "Unknown": "Unknown", "SDTV": "SDTV", "DVD": "DVD", "WEBDL1080p": "WEBDL-1080p", "HDTV720p": "HDTV-720p",
    "WEBDL720p": "WEBDL-720p", "Bluray720p": "Bluray-720p", "Bluray1080p": "Bluray-1080p", "WEBDL480p": "WEBDL-480p",
    "HDTV1080p": "HDTV-1080p", "RAWHD": "Raw-HD", "WEBRip480p": "WEBRip-480p", "Bluray480p": "Bluray-480p",
    "WEBRip720p": "WEBRip-720p", "WEBRip1080p": "WEBRip-1080p", "HDTV2160p": "HDTV-2160p",
    "WEBRip2160p": "WEBRip-2160p", "WEBDL2160p": "WEBDL-2160p", "Bluray2160p": "Bluray-2160p",
    "Bluray1080pRemux": "Bluray-1080p Remux", "Bluray2160pRemux": "Bluray-2160p Remux", "Bluray576p": "Bluray-576p",
}

LANGUAGE_NAMES = {
    "unknown": "Unknown", "english": "English", "french": "French", "spanish": "Spanish", "german": "German",
    "italian": "Italian", "danish": "Danish", "dutch": "Dutch", "japanese": "Japanese", "icelandic": "Icelandic",
    "chinese": "Chinese", "korean": "Korean", "latvian": "Latvian", "russian": "Russian", "polish": "Polish",
    "vietnamese": "Vietnamese", "swedish": "Swedish", "norwegian": "Norwegian", "finnish": "Finnish",
    "turkish": "Turkish", "portuguese": "Portuguese", "flemish": "Flemish", "greek": "Greek",
    "hungarian": "Hungarian", "hebrew": "Hebrew", "lithuanian": "Lithuanian", "czech": "Czech", "arabic": "Arabic",
    "hindi": "Hindi", "bulgarian": "Bulgarian", "malayalam": "Malayalam", "ukrainian": "Ukrainian",
    "slovak": "Slovak", "thai": "Thai", "portuguese_brazil": "Portuguese (Brazil)",
    "spanish_latino": "Spanish (Latino)", "romanian": "Romanian", "catalan": "Catalan",
}
if V5:
    LANGUAGE_NAMES.update({"urdu": "Urdu", "romansh": "Romansh", "georgian": "Georgian", "irish": "Irish"})


# ---------------------------------------------------------------------------------------------------------------
# C# attribute argument parsing

class Named:
    def __init__(self, name, value):
        self.name, self.value = name, value


def parse_args(text):
    """Parse the argument list of [TestCase(...)] into Python values."""
    p = Parser(text)
    args = []
    p.ws()
    while p.i < len(p.s):
        m = re.match(r"([A-Za-z_]\w*)\s*=(?!=)", p.s[p.i:])
        if m:
            p.i += m.end()
            p.ws()
            args.append(Named(m.group(1), p.value()))
        else:
            args.append(p.value())
        p.ws()
        if p.i < len(p.s) and p.s[p.i] == ",":
            p.i += 1
            p.ws()
    return [a for a in args if not isinstance(a, Named)]


class Parser:
    def __init__(self, s):
        self.s, self.i = s, 0

    def ws(self):
        while self.i < len(self.s) and self.s[self.i].isspace():
            self.i += 1

    def value(self):
        s, i = self.s, self.i
        if s.startswith('@"', i):
            j = i + 2
            out = []
            while True:
                if s[j] == '"':
                    if j + 1 < len(s) and s[j + 1] == '"':
                        out.append('"')
                        j += 2
                        continue
                    break
                out.append(s[j])
                j += 1
            self.i = j + 1
            return "".join(out)
        if s[i] == '"':
            j = i + 1
            out = []
            while s[j] != '"':
                if s[j] == "\\":
                    c = s[j + 1]
                    if c == "u":
                        out.append(chr(int(s[j + 2:j + 6], 16)))
                        j += 6
                        continue
                    if c == "x":
                        m = re.match(r"[0-9a-fA-F]{1,4}", s[j + 2:])
                        out.append(chr(int(m.group(0), 16)))
                        j += 2 + m.end()
                        continue
                    out.append({"n": "\n", "t": "\t", "r": "\r", "0": "\0", "\\": "\\", '"': '"', "'": "'"}[c])
                    j += 2
                    continue
                out.append(s[j])
                j += 1
            self.i = j + 1
            return "".join(out)
        m = re.match(r"new\s*(?:\w+\s*)?\[\s*(\d*)\s*\]\s*", s[i:])
        if m:
            self.i += m.end()
            if m.group(1) != "":
                return [None] * int(m.group(1)) if int(m.group(1)) else []
            assert s[self.i] == "{", s
            self.i += 1
            items = []
            self.ws()
            while s[self.i] != "}":
                items.append(self.value())
                self.ws()
                if s[self.i] == ",":
                    self.i += 1
                    self.ws()
            self.i += 1
            return items
        m = re.match(r"-?\d+(\.\d+)?[mdfMDF]?", s[i:])
        if m:
            self.i += m.end()
            t = m.group(0).rstrip("mdfMDF")
            return float(t) if "." in t else int(t)
        for word, v in (("true", True), ("false", False), ("null", None)):
            if s.startswith(word, i):
                self.i += len(word)
                return v
        m = re.match(r"[A-Za-z_][\w.]*", s[i:])
        if m:
            self.i += m.end()
            return ("ident", m.group(0))
        raise ValueError("cannot parse %r at %d" % (s, i))


def read_fixture(name):
    """Return ([(method, line, args)], {method: def_line}, lines) for a fixture file."""
    path = os.path.join(TESTS, name + ".cs")
    with open(path, encoding="utf-8-sig") as f:
        lines = f.read().split("\n")
    pending, cases, defs = [], [], {}
    for n, line in enumerate(lines, 1):
        st = line.strip()
        if st.startswith("//"):
            continue
        if st.startswith("[TestCase("):
            body = st[len("[TestCase("):]
            body = re.sub(r"\)\]\s*(//.*)?$", "", body)
            pending.append((n, parse_args(body)))
            continue
        m = re.match(r"public void (\w+)\(", st)
        if m:
            defs[m.group(1)] = n
            for ln, args in pending:
                cases.append((m.group(1), ln, args))
            pending = []
    return cases, defs, lines


# ---------------------------------------------------------------------------------------------------------------
# Helpers for expectations

def eps(e):
    """EpisodeNumbers.SingleOrDefault().Should().Be(e): one element e, or none when e is 0."""
    return [] if e == 0 else [e]


def as_os_agnostic(path):
    """NzbDrone.Test.Common AsOsAgnostic on Linux."""
    if len(path) > 2 and path[1] == ":":
        path = "/" + path.replace(":", "")
    return path.replace("\\", "/")


def path_combine(*parts):
    out = ""
    for p in parts:
        if p.startswith("/"):
            out = p
        elif out == "" or out.endswith("/"):
            out += p
        else:
            out += "/" + p
    return out


_XML_OK = re.compile("[^\x09\x0A\x0D\u0020-\uD7FF\uE000-\uFFFD]")


def xml_replace_unicode(s):
    """XmlCleaner.ReplaceUnicode. .NET strings are UTF-16, so astral characters (surrogate pairs) are removed."""
    return _XML_OK.sub("", s)


def single_ep(title, season, ep):
    return {"episodeNumbers": [ep], "seasonNumber": season, "seriesTitle": title,
            "absoluteEpisodeNumbers": [], "fullSeason": False}


def parse_title(expect):
    return ("parseTitle", expect)


def handler_table():
    H = {}

    # QualityParserFixture
    for m, q in [
        ("should_parse_sdtv_quality", "SDTV"), ("should_parse_dvd_quality", "DVD"),
        ("should_parse_webdl480p_quality", "WEBDL480p"), ("should_parse_bluray480p_quality", "Bluray480p"),
        ("should_parse_webrip480p_quality", "WEBRip480p"), ("should_parse_bluray576p_quality", "Bluray576p"),
        ("should_parse_hdtv720p_quality", "HDTV720p"), ("should_parse_hdtv1080p_quality", "HDTV1080p"),
        ("should_parse_hdtv2160p_quality", "HDTV2160p"), ("should_parse_webdl720p_quality", "WEBDL720p"),
        ("should_parse_webrip720p_quality", "WEBRip720p"), ("should_parse_webdl1080p_quality", "WEBDL1080p"),
        ("should_parse_webrip1080p_quality", "WEBRip1080p"), ("should_parse_webdl2160p_quality", "WEBDL2160p"),
        ("should_parse_webrip2160p_quality", "WEBRip2160p"), ("should_parse_bluray720p_quality", "Bluray720p"),
        ("should_parse_bluray1080p_quality", "Bluray1080p"),
        ("should_parse_bluray1080p_remux_quality", "Bluray1080pRemux"),
        ("should_parse_bluray2160p_quality", "Bluray2160p"),
        ("should_parse_bluray2160p_remux_quality", "Bluray2160pRemux"), ("should_parse_raw_quality", "RAWHD"),
        ("quality_parse", "Unknown"),
    ]:
        H[("QualityParserFixture", m)] = (lambda q: lambda a: (
            "parseQuality", a[0],
            {"quality.name": QUALITY_NAMES[q], "revision.version": 2 if a[1] else 1}))(q)

    def detection(src, res):
        return lambda a: ("parseQuality", a[0], {"sourceDetectionSource": src, "resolutionDetectionSource": res})

    H[("QualityParserFixture", "should_parse_full_quality_from_name")] = detection("name", "name")
    H[("QualityParserFixture", "should_parse_resolution_from_name")] = detection("unknown", "name")
    H[("QualityParserFixture", "should_parse_source_from_name")] = detection("name", "unknown")
    H[("QualityParserFixture", "should_parse_quality_from_extension")] = detection("extension", "extension")
    H[("QualityParserFixture", "should_parse_resolution_from_name_and_source_from_extension")] = \
        detection("extension", "name")
    H[("QualityParserFixture", "should_be_able_to_parse_repack")] = lambda a: (
        "parseQuality", a[0], {"revision.version": a[2], "revision.isRepack": a[1]})
    H[("QualityParserFixture", "should_be_able_to_parse_anime_version")] = lambda a: (
        "parseQuality", a[0], {"revision.version": a[1]})

    # AnimeVersionFixture, ExtendedQualityParserRegex
    H[("AnimeVersionFixture", "should_be_able_to_parse_repack")] = lambda a: (
        "parseQuality", a[0], {"revision.version": a[1]})
    H[("ExtendedQualityParserRegex", "should_parse_reality_from_title")] = lambda a: (
        "parseQuality", a[0], {"revision.real": a[1]})
    H[("ExtendedQualityParserRegex", "should_parse_version_from_title")] = lambda a: (
        "parseQuality", a[0], {"revision.version": a[1]})

    # AbsoluteEpisodeNumberParserFixture
    F = "AbsoluteEpisodeNumberParserFixture"
    H[(F, "should_parse_absolute_numbers")] = lambda a: ("parseTitle", a[0], {
        "absoluteEpisodeNumbers": [a[2]], "seasonNumber": a[3], "episodeNumbers": eps(a[4]),
        "seriesTitle": a[1], "fullSeason": False})
    H[(F, "should_parse_absolute_specials")] = lambda a: ("parseTitle", a[0], {
        "absoluteEpisodeNumbers": [a[2]], "seasonNumber": 0, "episodeNumbers": [], "seriesTitle": a[1],
        "fullSeason": False, "special": True})
    H[(F, "should_parse_multi_episode_absolute_numbers")] = lambda a: ("parseTitle", a[0], {
        "absoluteEpisodeNumbers|anyOrder": list(range(a[2], a[3] + 1)), "seriesTitle": a[1], "fullSeason": False})
    H[(F, "should_parse_anime_season_packs")] = lambda a: ("parseTitle", a[0], {
        "absoluteEpisodeNumbers": [], "seriesTitle": a[1], "fullSeason": True, "seasonNumber": a[2]})
    H[(F, "should_handle_anime_recap_numbering")] = lambda a: ("parseTitle", a[0], {
        "seriesTitle": a[1], "absoluteEpisodeNumbers": [], "specialAbsoluteEpisodeNumbers|anyOrder": [a[2]],
        "fullSeason": False})
    H[(F, "should_handle_ambiguously_named_anime_files_in_batch_release")] = lambda a: (
        "parsePath", path_combine(as_os_agnostic("C:\\Test"), a[0], a[1]), {
            "absoluteEpisodeNumbers": [a[3]], "seasonNumber": 0, "episodeNumbers": [], "seriesTitle": a[2],
            "fullSeason": False})
    H[(F, "should_parse_absolute_followed_by_standard_as_standard")] = lambda a: ("parseTitle", a[0], {
        "episodeNumbers": [a[4]], "seasonNumber": a[3], "seriesTitle": a[1],
        "specialAbsoluteEpisodeNumbers": [a[2]], "absoluteEpisodeNumbers": [], "fullSeason": False})

    # AnimeMetadataParserFixture
    H[("AnimeMetadataParserFixture", "should_parse_releasegroup_and_hash")] = lambda a: (
        "parseTitle", a[0], {"|not": None, "releaseGroup": a[1], "releaseHash": a[2]})
    H[("AnimeMetadataParserFixture", "should_parse_release_hashes_with_10b_or_8b")] = lambda a: (
        "parseTitle", a[0], {"|not": None, "releaseHash": a[1]})

    # CrapParserFixture
    F = "CrapParserFixture"
    for m in ("should_not_parse_crap", "should_not_parse_file_name_without_proper_spacing",
              "should_not_parse_invalid_release_name", "should_not_parse_special_with_part_number"):
        H[(F, m)] = lambda a: ("parseTitle", a[0], {"": None})
    H[(F, "should_parse_unknown_formats_without_error")] = lambda a: ("parseTitle", a[0], {"|not": None})

    def random_titles(length):
        # The C# test draws 500 strings from new Random(); a fixed seed keeps the data stable.
        rng = random.Random(length)
        charset = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
        return ["".join(charset[rng.randrange(len(charset))] for _ in range(length)) for _ in range(500)]

    H[(F, "should_not_parse_random")] = lambda a: (
        "parseTitleAll", random_titles(a[0]), {"": [None] * 500})

    # DailyEpisodeParserFixture
    F = "DailyEpisodeParserFixture"
    H[(F, "should_parse_daily_episode")] = lambda a: ("parseTitle", a[0], {
        "seriesTitle": a[1], "airDate": "%04d-%02d-%02d" % (a[2], a[3], a[4]), "episodeNumbers": [],
        "absoluteEpisodeNumbers": [], "fullSeason": False})
    H[(F, "should_parse_daily_episode_with_multiple_parts")] = lambda a: ("parseTitle", a[0], {
        "seriesTitle": a[1], "airDate": "%04d-%02d-%02d" % (a[2], a[3], a[4]), "episodeNumbers": [],
        "absoluteEpisodeNumbers": [], "fullSeason": False, "dailyPart": a[5]})
    H[(F, "should_not_accept_ancient_daily_series")] = lambda a: (
        "parseTitleExpand", {"template": a[0], "year": 1950, "month": "10", "day": "14"}, {"": None})
    H[(F, "should_not_accept_future_dates")] = lambda a: (
        "parseTitleExpand", {"template": a[0], "daysFromNow": 2}, {"": None})
    H[(F, "should_not_parse_ambiguous_daily_episode")] = lambda a: ("parseTitle", a[0], {"": None})

    # IsPossibleSpecialEpisodeFixture
    F = "IsPossibleSpecialEpisodeFixture"
    for m in ("IsPossibleSpecialEpisode_should_be_true", "IsPossibleSpecialEpisode_should_be_true_if_e00_special"):
        H[(F, m)] = lambda a: ("parseTitle", a[0], {"isPossibleSpecialEpisode": True})
    H[(F, "IsPossibleSpecialEpisode_should_be_false_for_Special_in_title")] = lambda a: (
        "parseTitle", a[0], {"isPossibleSpecialEpisode": False})

    # IsoLanguagesFixture
    F = "IsoLanguagesFixture"
    H[(F, "should_return_iso_language_for_English")] = lambda a: ("findIsoLanguage", a[0], {"language.name": "English"})
    H[(F, "unknown_or_invalid_code_should_return_null")] = lambda a: ("findIsoLanguage", a[0], {"": None})
    H[(F, "should_return_portuguese")] = lambda a: ("findIsoLanguage", a[0], {"language.name": "Portuguese"})
    H[(F, "should_not_return_portuguese")] = lambda a: ("findIsoLanguage", a[0], {"": None})
    H[(F, "should_lookup_cze_via_alternate_iso_code_mapping")] = lambda a: (
        "findIsoLanguage", a[0], {"language.name": "Czech"})

    # LanguageParserFixture
    F = "LanguageParserFixture"
    H[(F, "should_parse_subtitle_language_unknown")] = lambda a: ("parseSubtitleLanguage", a[0], {"name": "Unknown"})
    H[(F, "should_parse_subtitle_language_english")] = lambda a: ("parseSubtitleLanguage", a[0], {"name": "English"})
    for key, name in LANGUAGE_NAMES.items():
        H[(F, "should_parse_language_" + key)] = (lambda name: lambda a: (
            "parseLanguages", a[0], {"*.name|contains": name}))(name)
    H[(F, "should_parse_hindi_and_english")] = lambda a: (
        "parseLanguages", a[0], {"*.name|anyOrder": ["Hindi", "English"]})
    H[(F, "should_parse_spanish_and_catalan")] = lambda a: (
        "parseLanguages", a[0], {"*.name|anyOrder": ["Spanish", "Catalan"]})
    H[(F, "should_parse_english_spanish_and_catalan")] = lambda a: (
        "parseLanguages", a[0], {"*.name|anyOrder": ["English", "Spanish", "Catalan"]})
    H[(F, "should_add_original_language_to_german_release_with_dl_tag")] = lambda a: ("parseTitle", a[0], {
        "languages|count": 2, "languages.*.name|contains": "German", "languages.*.name|contains#2": "Original"})
    H[(F, "should_not_add_original_language_to_german_release_when_title_contains_web_dl")] = lambda a: (
        "parseTitle", a[0], {"languages|count": 1, "languages.*.name|contains": "German"})
    H[(F, "should_add_original_language_and_english_to_german_release_with_ml_tag")] = lambda a: (
        "parseTitle", a[0], {"languages|count": 3, "languages.*.name|contains": "German",
                             "languages.*.name|contains#2": "Original", "languages.*.name|contains#3": "English"})
    H[(F, "should_parse_original_title_from_release_name")] = lambda a: (
        "parseTitle", a[0], {"languages|count": 1, "languages.*.name|contains": "Original"})
    H[(F, "should_parse_english_and_ukranian")] = lambda a: ("parseTitle", a[0], {
        "languages|count": 2, "languages.*.name|contains": "Ukrainian", "languages.*.name|contains#2": "English"})
    H[(F, "should_parse_turkish_and_ukranian")] = lambda a: ("parseTitle", a[0], {
        "languages|count": 2, "languages.*.name|contains": "Ukrainian", "languages.*.name|contains#2": "Turkish"})
    H[(F, "should_parse_title_and_tags")] = lambda a: ("parseSubtitleLanguageInformation", a[0], {
        "languageTags|anyOrder": a[1], "title": a[2], "language.name": a[3]})
    H[(F, "should_not_parse_false_title")] = lambda a: ("parseSubtitleLanguageInformation", a[0], {
        "language.name": "Unknown", "languageTags": [], "rawTitle": None})

    # MiniSeriesEpisodeParserFixture
    F = "MiniSeriesEpisodeParserFixture"
    H[(F, "should_parse_mini_series_episode")] = lambda a: ("parseTitle", a[0], single_ep(a[1], 1, a[2]))
    H[(F, "should_parse_korean_series_episode")] = lambda a: ("parseTitle", a[0], single_ep(a[1], 1, a[2]))

    # MultiEpisodeParserFixture
    H[("MultiEpisodeParserFixture", "should_parse_multiple_episodes")] = lambda a: ("parseTitle", a[0], {
        "seasonNumber": a[2], "episodeNumbers|anyOrder": a[3], "seriesTitle": a[1], "absoluteEpisodeNumbers": [],
        "fullSeason": False})

    # NormalizeEpisodeTitleFixture
    H[("NormalizeEpisodeTitleFixture", "should_normalize_episode_title")] = lambda a: (
        "normalizeEpisodeTitle", a[0], {"": a[1]})

    # NormalizeSeriesTitleFixture
    F = "NormalizeSeriesTitleFixture"
    for m in ("should_normalize_series_title", "should_remove_special_characters_and_casing",
              "should_not_remove_from_the_beginning_of_the_title",
              "should_replace_percent_sign_with_percent_following_numbers"):
        H[(F, m)] = lambda a: ("cleanSeriesTitle", a[0], {"": a[1]})
    H[(F, "should_remove_common_words_from_middle_of_title")] = lambda a: (
        "cleanSeriesTitleAll", [f.replace("{0}", a[0]) for f in ("word.{0}.word", "word {0} word", "word-{0}-word")],
        {"": ["wordword"] * 3})
    H[(F, "should_not_remove_common_words_from_end_of_title")] = lambda a: (
        "cleanSeriesTitleAll", [f.replace("{0}", a[0]) for f in ("word.word.{0}", "word-word-{0}", "word-word {0}")],
        {"": ["wordword" + a[0].lower()] * 3})
    H[(F, "should_not_remove_common_words_in_the_middle_of_word")] = lambda a: (
        "cleanSeriesTitleAll", [f.replace("{0}", a[0]) for f in (
            "word.{0}word", "word {0}word", "word-{0}word", "word{0}.word", "word{0}-word", "word{0}-word")],
        {"": ["word" + a[0].lower() + "word"] * 6})
    H[(F, "should_not_clean_word_from_beginning_of_string")] = lambda a: (
        "cleanSeriesTitleAll", [f.replace("{0}", a[0]) for f in ("{0}.word.word", "{0}-word-word", "{0} word word")],
        {"": [a[0] + "wordword"] * 3})

    # ParserFixture
    F = "ParserFixture"
    H[(F, "should_parse_series_name")] = lambda a: ("parseSeriesName", a[0], {"|cleanEquals": a[1]})
    H[(F, "should_parse_series_title_info")] = lambda a: ("parseTitle", a[0], {
        "seriesTitleInfo.titleWithoutYear": a[1], "seriesTitleInfo.year": a[2] if len(a) > 2 else 0})
    H[(F, "should_clean_up_invalid_path_characters")] = lambda a: ("parseTitle", a[0], {})
    H[(F, "should_remove_request_info_from_title")] = lambda a: ("parseTitle", a[0], {"seriesTitle": a[1]})
    H[(F, "should_parse_quality_from_extension")] = lambda a: ("parseTitleQualityDetection", a[0], {
        "quality.id|not": 0, "sourceDetectionSource": "extension", "resolutionDetectionSource": "extension"})
    H[(F, "should_parse_releasetitle")] = lambda a: ("parseTitle", a[0], {"releaseTitle": a[1]})
    H[(F, "should_parse_multiple_series_titles")] = lambda a: ("parseTitle", a[0], {
        "seriesTitleInfo.allTitles|anyOrder": list(a[1:])})
    H[(F, "should_parse_language_after_parsing_title")] = lambda a: ("parseTitle", a[0], {
        "languages|count": 1, "languages.*.name|contains": a[1]})

    # PathParserFixture
    F = "PathParserFixture"
    H[(F, "should_parse_from_path")] = lambda a: ("parsePath", as_os_agnostic(a[0]), {
        "episodeNumbers": [a[2]], "seasonNumber": a[1], "absoluteEpisodeNumbers": [], "fullSeason": False})
    H[(F, "should_parse_multi_episode_from_path")] = lambda a: ("parsePath", as_os_agnostic(a[0]), {
        "seriesTitle": a[1], "episodeNumbers|count": len(a[3]), "seasonNumber": a[2],
        "episodeNumbers|anyOrder": a[3], "absoluteEpisodeNumbers": [], "fullSeason": False})

    # ReleaseGroupParserFixture
    F = "ReleaseGroupParserFixture"
    for m in ("should_parse_release_group", "should_parse_exception_release_group",
              "should_not_include_language_in_release_group", "should_not_include_repost_in_release_group",
              "should_parse_anime_release_groups"):
        H[(F, m)] = lambda a: ("parseReleaseGroup", a[0], {"": a[1]})
    H[(F, "should_not_parse_anime_hash_as_release_group")] = lambda a: ("parseReleaseGroup", a[0], {"": None})

    # SceneCheckerFixture
    H[("SceneCheckerFixture", "should_return_true_for_scene_names")] = lambda a: ("isSceneTitle", a[0], {"": True})
    H[("SceneCheckerFixture", "should_return_false_for_non_scene_names")] = lambda a: (
        "isSceneTitle", a[0], {"": False})

    # SeasonParserFixture
    F = "SeasonParserFixture"
    base = lambda a: {"seasonNumber": a[2], "seriesTitle": a[1], "episodeNumbers": [], "absoluteEpisodeNumbers": []}
    H[(F, "should_parse_full_season_release")] = lambda a: ("parseTitle", a[0], dict(base(a), fullSeason=True))
    H[(F, "should_parse_season_extras")] = lambda a: (
        "parseTitle", a[0], dict(base(a), fullSeason=True, isSeasonExtra=True))
    H[(F, "should_parse_season_subpack")] = lambda a: (
        "parseTitle", a[0], dict(base(a), fullSeason=True, isSeasonExtra=True))
    H[(F, "should_parse_partial_season_release")] = lambda a: (
        "parseTitle", a[0], dict(base(a), fullSeason=False, isPartialSeason=True, seasonPart=a[3]))
    H[(F, "should_parse_multi_season_release")] = lambda a: (
        "parseTitle", a[0], dict(base(a), fullSeason=True, isPartialSeason=False, isMultiSeason=True))

    # SingleEpisodeParserFixture
    F = "SingleEpisodeParserFixture"
    H[(F, "should_parse_single_episode")] = lambda a: ("parseTitle", a[0], single_ep(a[1], a[2], a[3]))
    H[(F, "should_parse_japanese_variety_show_format")] = lambda a: ("parseTitle", a[0], single_ep(a[1], a[2], a[3]))
    H[(F, "should_parse_decimal_number_as_special")] = lambda a: (
        "parseTitle", a[0], dict(single_ep(a[1], a[2], a[3]), special=True))
    H[(F, "should_parse_split_episode")] = lambda a: (
        "parseTitle", a[0], dict(single_ep(a[1], a[2], a[3]), isSplitEpisode=True))

    # UnicodeReleaseParserFixture
    F = "UnicodeReleaseParserFixture"
    for m in ("should_parse_chinese_anime_releases", "should_parse_chinese_anime_season_episode_releases",
              "should_parse_unbracketed_chinese_anime_releases", "should_parse_false_positive_chinese_anime_releases"):
        H[(F, m)] = lambda a: ("parseTitle", xml_replace_unicode(a[0]), {
            "releaseGroup": a[2], "absoluteEpisodeNumbers": [a[3]], "seriesTitle": a[1], "fullSeason": False})
    H[(F, "should_parse_chinese_multiepisode_releases")] = lambda a: ("parseTitle", xml_replace_unicode(a[0]), {
        "releaseGroup": a[2], "absoluteEpisodeNumbers|anyOrder": a[3], "seriesTitle": a[1], "fullSeason": False})
    H[(F, "should_parse_gm_team_releases_and_files")] = lambda a: ("parseTitle", a[0], {
        "absoluteEpisodeNumbers": [a[2]], "seriesTitle": a[1], "fullSeason": False, "releaseGroup": "GM-Team"})
    H[(F, "should_parse_unicode_digits")] = lambda a: ("parseTitle", a[0], {
        "seriesTitle": a[1], "absoluteEpisodeNumbers|anyOrder": [a[2]], "specialAbsoluteEpisodeNumbers": [],
        "fullSeason": False})

    # UrlFixture
    H[("UrlFixture", "should_not_parse_url_in_name")] = lambda a: ("parseSeriesName", a[0], {"|cleanEquals": a[1]})
    H[("UrlFixture", "should_not_parse_url_in_group")] = lambda a: ("parseReleaseGroup", a[0], {"": a[1]})

    if V5:
        v5_handlers(H)
    return H


def v5_handlers(H):
    """The handlers v5 adds or changes (Sonarr develop @ a6171f0ca)."""
    F = "AbsoluteEpisodeNumberParserFixture"
    H[(F, "should_parse_absolute_specials_without_absolute_number")] = lambda a: ("parseTitle", a[0], {
        "|not": None, "absoluteEpisodeNumbers": [], "seasonNumber": 0, "episodeNumbers": [], "seriesTitle": a[1],
        "fullSeason": False, "special": True})

    F = "DailyEpisodeParserFixture"
    H.pop((F, "should_not_accept_future_dates"), None)
    H[(F, "should_accept_dates_within_future_limit")] = lambda a: (
        "parseTitleExpandDate", {"template": a[0], "daysFromNow": 2}, {"isNull": False, "airDateIsTarget": True})
    talk = "A.Talk.Show.{year}.{month}.{day}.1080p.WEB-DL.H264-Sonarr"
    H[(F, "should_parse_episodes_released_before_their_air_date")] = lambda a: (
        "parseTitleExpandDate", {"template": talk, "daysFromNow": a[0]}, {"airDateIsTarget": True})
    H[(F, "should_not_parse_dates_beyond_future_limit")] = lambda a: (
        "parseTitleExpandDate", {"template": talk, "daysFromNow": a[0]}, {"isNull": True})
    H[(F, "should_parse_daily_episode_using_short_month_format")] = lambda a: ("parseTitle", a[0], {
        "|not": None, "seriesTitle": a[1], "airDate": "%04d-%02d-%02d" % (a[2], a[3], a[4]), "episodeNumbers": [],
        "absoluteEpisodeNumbers": [], "fullSeason": False})

    F = "IsoLanguagesFixture"
    for m, name in (("should_return_urdu", "Urdu"), ("should_return_romansh", "Romansh"),
                    ("should_return_georgian", "Georgian"), ("should_return_irish", "Irish")):
        H[(F, m)] = (lambda name: lambda a: ("findIsoLanguage", a[0], {"language.name": name}))(name)

    F = "LanguageParserFixture"
    H[(F, "should_parse_language_french_english")] = lambda a: (
        "parseLanguages", a[0], {"*.name|contains": "French", "*.name|contains#2": "English"})
    H[(F, "should_parse_azerbaijani")] = lambda a: (
        "parseTitle", a[0], {"languages|count": 1, "languages.*.name|contains": "Azerbaijani"})
    H[(F, "should_parse_uzbek")] = lambda a: (
        "parseTitle", a[0], {"languages|count": 1, "languages.*.name|contains": "Uzbek"})
    H[(F, "should_parse_language_russian_and_georgian")] = lambda a: (
        "parseLanguages", a[0], {"*.name|anyOrder": ["Russian", "Georgian"]})
    H[(F, "should_parse_language_english_french_german_italian_portuguese_spanish")] = lambda a: (
        "parseLanguages", a[0], {"*.name|anyOrder": ["English", "French", "German", "Italian", "Portuguese",
                                                     "Spanish"]})

    H[("ParsedEpisodeInfoFixture", "should_format_full_season")] = lambda a: (
        "parsedEpisodeInfoStringContains",
        {"fields": {"seriesTitle": "Series Title", "seasonNumbers": a[0], "fullSeason": True}, "expected": a[1]},
        {"": True})

    F = "ReleaseGroupParserFixture"
    H[(F, "should_not_parse_episode_title_as_release_group")] = lambda a: ("parseReleaseGroup", a[0], {"": None})

    F = "SeasonParserFixture"
    H[(F, "should_parse_multi_season_release")] = lambda a: ("parseTitle", a[0], {
        "seriesTitle": a[1], "episodeNumbers": [], "absoluteEpisodeNumbers": [], "fullSeason": True,
        "isPartialSeason": False, "isMultiSeason": True, "seasonNumbers": a[2]})
    H[(F, "should_not_be_multi_season_for_a_single_season_release")] = lambda a: (
        "parseTitle", a[0], {"isMultiSeason": False, "seasonNumber": a[1]})

    F = "SeasonTitleParserFixture"
    H[(F, "should_return_the_title_when_there_is_no_season_or_episode")] = lambda a: ("parseSeasonTitle", a[0], {
        "|not": None, "seriesTitle": a[1], "fullSeason": True, "isSeasonTitle": True, "seasonNumbers": [],
        "episodeNumbers": [], "absoluteEpisodeNumbers": []})
    H[(F, "should_populate_the_fields_the_decision_engine_reads")] = lambda a: ("parseSeasonTitle", a[0], {
        "quality|not": None, "quality.quality|not": None, "languages.#|not": 0})
    for m in ("should_not_return_a_title_when_stripping_leaves_metadata", "should_not_return_a_title_for_an_extra"):
        H[(F, m)] = lambda a: ("parseSeasonTitle", a[0], {"": None})
    H[(F, "should_not_flag_a_release_the_regexes_can_parse")] = lambda a: (
        "parseTitle", a[0], {"isSeasonTitle": False})
    H[(F, "should_leave_parse_title_rejecting_what_it_rejects_today")] = lambda a: ("parseTitle", a[0], {"": None})

    H[("SubGroupParserFixture", "should_parse_sub_group_from_title_as_release_group")] = lambda a: (
        "parseTitle", a[0], {"|not": None, "releaseGroup": a[1]})

    F = "UnicodeReleaseParserFixture"
    H[(F, "should_parse_full_season_releases")] = lambda a: ("parseTitle", xml_replace_unicode(a[0]), {
        "|not": None, "releaseGroup": a[2], "episodeNumbers": [], "absoluteEpisodeNumbers": [], "seriesTitle": a[1],
        "fullSeason": True, "seasonNumber": a[3]})
    H[(F, "should_parse_multi_episode_release")] = lambda a: ("parseTitle", xml_replace_unicode(a[0]), {
        "seasonNumber": a[3], "episodeNumbers|anyOrder": a[4], "seriesTitle": a[1], "absoluteEpisodeNumbers": [],
        "fullSeason": False})


def v5_expect(expect):
    """v5 asserts SeasonNumbers.Should().Equal(n) where v4 asserted SeasonNumber.Should().Be(n)."""
    out = {}
    for k, v in expect.items():
        if k == "seasonNumber":
            out["seasonNumbers"] = [v]
        else:
            out[k] = v
    return out


def v5_args(inp):
    """ParsedEpisodeInfo initialisers: SeasonNumbers = [n] for SeasonNumber = n."""
    if isinstance(inp, dict) and "seasonNumber" in inp:
        inp = dict(inp)
        inp["seasonNumbers"] = [inp.pop("seasonNumber")]
    return inp


# [Test] methods and TestCaseSource items, by fixture: (method, [(call, input, expect)]).
def plain_tests(lines):
    T = {}

    self_cases = ["SDTV", "DVD", "WEBDL480p", "Bluray480p", "Bluray576p", "HDTV720p", "HDTV1080p", "HDTV2160p",
                  "WEBDL720p", "WEBDL1080p", "WEBDL2160p", "Bluray720p", "Bluray1080p", "Bluray2160p",
                  "Bluray1080pRemux", "Bluray2160pRemux"]
    other = [("SD TV", "SDTV"), ("SD DVD", "DVD"), ("480p WEB-DL", "WEBDL480p"), ("HD TV", "HDTV720p"),
             ("1080p HD TV", "HDTV1080p"), ("2160p HD TV", "HDTV2160p"), ("720p WEB-DL", "WEBDL720p"),
             ("1080p WEB-DL", "WEBDL1080p"), ("2160p WEB-DL", "WEBDL2160p"), ("720p BluRay", "Bluray720p"),
             ("1080p BluRay", "Bluray1080p"), ("2160p BluRay", "Bluray2160p"), ("1080p Remux", "Bluray1080pRemux"),
             ("2160p Remux", "Bluray2160pRemux")]
    T["QualityParserFixture"] = [
        ("parsing_our_own_quality_enum_name", [
            ("parseQuality", "My series S01E01 [%s]" % QUALITY_NAMES[q], {"quality.name": QUALITY_NAMES[q]})
            for q in self_cases]),
        ("should_parse_quality_from_other_source", [
            ("parseQualityAll", ["My series S01E01 " + s.replace(" ", c) for c in "-. _"],
             {"*.quality.name": [QUALITY_NAMES[q]] * 4, "*.revision.version": [1] * 4})
            for s, q in other]),
    ]

    hashed = [
        (r"C:\Test\Some.Hashed.Release.S01E01.720p.WEB-DL.AAC2.0.H.264-Mercury\0e895c37245186812cb08aab1529cf8ee389dd05.mkv", "Some Hashed Release", "WEBDL720p", "Mercury"),
        (r"C:\Test\0e895c37245186812cb08aab1529cf8ee389dd05\Some.Hashed.Release.S01E01.720p.WEB-DL.AAC2.0.H.264-Mercury.mkv", "Some Hashed Release", "WEBDL720p", "Mercury"),
        (r"C:\Test\Fake.Dir.S01E01-Test\yrucreM-462.H.0.2CAA.LD-BEW.p027.10E10S.esaeleR.dehsaH.emoS.mkv", "Some Hashed Release", "WEBDL720p", "Mercury"),
        (r"C:\Test\Fake.Dir.S01E01-Test\yrucreM-LN 1.5DD LD-BEW P0801 10E10S esaeleR dehsaH emoS.mkv", "Some Hashed Release", "WEBDL1080p", "Mercury"),
        (r"C:\Test\Title.S01E10.DVDRip.XviD-SONARR\AHFMZXGHEWD660.mkv", "Title", "DVD", "SONARR"),
        (r"C:\Test\Show Title.S02E12.1080p.BluRay.x264-SONARR\Backup_72023S02-12.mkv", "Show Title", "Bluray1080p", "SONARR"),
        (r"C:\Test\Title S04E08 Chupacabra 720p WEB-DL DD5 1 H 264-ECI\123.mkv", "Title", "WEBDL720p", "ECI"),
        (r"C:\Test\Title S04E08 Chupacabra 720p WEB-DL DD5 1 H 264-ECI\abc.mkv", "Title", "WEBDL720p", "ECI"),
        (r"C:\Test\Title S04E08 Chupacabra 720p WEB-DL DD5 1 H 264-ECI\b00bs.mkv", "Title", "WEBDL720p", "ECI"),
        (r"C:\Test\The.Show.Title.S02E23.720p.HDTV.x264-NZBgeek/cgajsofuejsa501.mkv", "The Show Title", "HDTV720p", "NZBgeek"),
        (r"C:\Test\Show.Title.S03E04.1080p.WEB-DL.DD5.1.H264-RARBG\170424_26.mkv", "Show Title", "WEBDL1080p", "RARBG"),
        (r"C:\Test\XxQVHK4GJMP3n2dLpmhW\XxQVHK4GJMP3n2dLpmhW\MKV\010E70S.nuF.fo.snoS.mkv", "Sons of Fun", "HDTV720p", None),
        (r"50E50S.denorD.mkv", "Droned", "HDTV720p", None),
        (r"C:\Test\XxQVHK4GJMP3n2dLpmhW\XxQVHK4GJMP3n2dLpmhW\50E50S.denorD.mkv", "Droned", "HDTV720p", None),
        (r"C:\Test\Series.Title.S02E24.720p.HDTV.H.264\abc.xyz.af6021c37f7852.mkv", "Series Title", "HDTV720p", None),
        (r"C:\Test\tv\Series.Title.S04E03.Episode.Name.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb\cwnOJYks5E2WP7zGuzPkdkRK3JkWw0.mkv", "Series Title", "WEBDL1080p", "NTb"),
        (r"C:\Test\tv\Series.Title.2017.S04E07.Episode.Name.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb/s2e2fYzx5xJhuBjHn5ZXE07Ebi.mkv", "Series Title 2017", "WEBDL1080p", "NTb"),
        (r"C:\Test\tv\Series.Title.S04E03.Episode.Name.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb\zM0vnVU1SoV4uwTihfzTHkYmJPBemgN3MqXG1fY.mkv", "Series Title", "WEBDL1080p", "NTb"),
        (r"C:\Test\tv\Series.Title.2019.S02E02.The.Episode.of.Names.1080p.AMZN.WEB-DL.DDP5.1.H.264-NTb/46XuzmawYQeUBwNdH2Hw2996.mkv", "Series Title 2019", "WEBDL1080p", "NTb"),
    ]
    if V5:
        hashed.append((r"C:\Test\Fake.Dir.S01E01-Test\11E-20S seires.mkv", "series", "HDTV720p", None))
    T["HashedReleaseFixture"] = [
        ("should_properly_parse_hashed_releases", [
            ("parsePath", as_os_agnostic(p), {"seriesTitle": t, "quality.quality.name": QUALITY_NAMES[q],
                                              "releaseGroup": g})
            for p, t, q, g in hashed]),
    ]

    T["IsPossibleSpecialEpisodeFixture"] = [
        ("should_not_treat_files_without_a_series_title_as_a_special", [
            ("parsedEpisodeInfo", {"episodeNumbers": [7], "seasonNumber": 1, "seriesTitle": ""},
             {"isPossibleSpecialEpisode": False})]),
        ("should_return_true_when_episode_numbers_is_empty", [
            ("parsedEpisodeInfo", {"seasonNumber": 1, "seriesTitle": ""}, {"isPossibleSpecialEpisode": True})]),
    ]

    T["ParserFixture"] = [
        ("should_remove_accents_from_title", [("cleanSeriesTitle", "Seri\u00E0es", {"": "seriaes"})]),
    ]

    # 100 chained MD5 hashes of "CRAPPY TEST SEED", upper-case hex without dashes, as BitConverter.ToString gives.
    md5s, h = [], "CRAPPY TEST SEED"
    for _ in range(100):
        h = hashlib.md5(h.encode("utf-8")).hexdigest().upper()
        md5s.append(h)
    T["CrapParserFixture"] = [
        ("should_not_parse_md5", [("parseTitleAll", md5s, {"": [None] * 100})]),
    ]

    T["DailyEpisodeParserFixture"] = [
        ("should_fail_if_episode_is_far_in_future", [
            ("parseTitleExpand", {"template": "{year}.{month}.{day} - A Talk Show - HD TV.mkv", "daysFromNow": 2},
             {"": None})]),
    ]
    if V5:
        T["DailyEpisodeParserFixture"] = [
            ("should_parse_daily_episode_into_season_zero", [
                ("parseTitle", "A.Late.Talk.Show.2010.10.11.Johnny.Knoxville.iTouch-MW", {"seasonNumbers": [0]})]),
        ]
        T["ParsedEpisodeInfoFixture"] = [
            ("should_be_possible_scene_season_special_for_e00_in_a_regular_season", [
                ("parsedEpisodeInfo", {"seasonNumbers": [2], "episodeNumbers": [0]},
                 {"isPossibleSceneSeasonSpecial": True})]),
            ("should_not_be_possible_scene_season_special_in_season_zero", [
                ("parsedEpisodeInfo", {"seasonNumbers": [0], "episodeNumbers": [0]},
                 {"isPossibleSceneSeasonSpecial": False})]),
            ("should_not_be_possible_scene_season_special_without_a_season", [
                ("parsedEpisodeInfo", {"seasonNumbers": [], "episodeNumbers": [0]},
                 {"isPossibleSceneSeasonSpecial": False})]),
            ("should_not_be_possible_special_episode_without_a_season_or_series_title", [
                ("parsedEpisodeInfo", {"seriesTitle": "", "seasonNumbers": [], "episodeNumbers": [7]},
                 {"isPossibleSpecialEpisode": False})]),
            ("should_be_possible_special_episode_in_season_zero_without_series_title", [
                ("parsedEpisodeInfo", {"seriesTitle": "", "seasonNumbers": [0], "episodeNumbers": [7]},
                 {"isPossibleSpecialEpisode": True})]),
            ("should_format_episode_with_first_season", [
                ("parsedEpisodeInfoStringContains",
                 {"fields": {"seriesTitle": "Series Title", "seasonNumbers": [2], "episodeNumbers": [5, 6]},
                  "expected": "S02E05-06"}, {"": True})]),
        ]

    T["ReleaseGroupParserFixture"] = [
        # The path has Windows separators and no AsOsAgnostic: on Linux the whole string is the file name.
        ("should_not_include_extension_in_release_group", [
            ("parsePath", r"C:\Test\Doctor.Series.2005.s01e01.internal.bdrip.x264-archivist.mkv",
             {"releaseGroup": "archivist"})]),
    ]

    T["NormalizeSeriesTitleFixture"] = [
        ("should_remove_a_from_middle_of_title", [
            ("cleanSeriesTitleAll", ["word.a.word", "word a word", "word-a-word"], {"": ["wordword"] * 3})]),
        ("should_not_clean_trailing_a", [("cleanSeriesTitle", "Series Title A", {"": "seriestitlea"})]),
    ]

    T["SeasonParserFixture"] = [
        ("should_not_parse_season_folders", [("parseTitle", "Season 3", {"": None})]),
    ]

    title = "Series.Title.S01E01.pilot.720p.hdtv"
    year_title = "Series.Title.2004.S01E01.pilot.720p.hdtv"
    T["SeriesTitleInfoFixture"] = [
        ("should_have_year_zero_when_title_doesnt_have_a_year", [
            ("parseTitle", title, {"seriesTitleInfo.year": 0})]),
        ("should_have_same_title_for_title_and_title_without_year_when_title_doesnt_have_a_year", [
            # Title.Should().Be(TitleWithoutYear): both are "Series Title" for this input.
            ("parseTitle", title, {"seriesTitleInfo.title": "Series Title",
                                   "seriesTitleInfo.titleWithoutYear": "Series Title"})]),
        ("should_have_year_when_title_has_a_year", [("parseTitle", year_title, {"seriesTitleInfo.year": 2004})]),
        ("should_have_year_in_title_when_title_has_a_year", [
            ("parseTitle", year_title, {"seriesTitleInfo.title": "Series Title 2004"})]),
        ("should_title_without_year_should_not_contain_year", [
            ("parseTitle", year_title, {"seriesTitleInfo.titleWithoutYear": "Series Title"})]),
    ]

    T["ValidateParsedEpisodeInfoFixture"] = [
        ("should_return_true_if_episode_info_is_not_daily", [
            ("validateForSeriesType", {"parsedEpisodeInfo": {}, "seriesType": "standard"}, {"": True})]),
        ("should_return_true_if_episode_info_is_daily_for_daily_series", [
            ("validateForSeriesType", {"parsedEpisodeInfo": {"airDate": "2018-05-21"}, "seriesType": "daily"},
             {"": True})]),
        ("should_return_false_if_episode_info_is_daily_for_standard_series", [
            ("validateForSeriesType", {"parsedEpisodeInfo": {"airDate": "2018-05-21"}, "seriesType": "standard"},
             {"": False})]),
    ]
    return T


# Cases left out on purpose: fixture/method -> reason. They are listed in the report.
EXCLUDED_METHODS = {
    ("ValidateParsedEpisodeInfoFixture", "should_not_log_warning_if_warnIfInvalid_is_false"):
        "asserts only the logged warnings; the port does not log",
}

EXCLUDED_FIXTURES_V5 = {
    "ReleaseComparerFixture": "ReleaseComparer.SameNzb compares blocklisted Usenet releases (indexer, publish date); "
                              "not a parser function, and Theatarr has no Usenet blocklist",
}

FIXTURES = [
    "QualityParserFixture", "ExtendedQualityParserRegex", "AnimeVersionFixture",
    "LanguageParserFixture", "IsoLanguagesFixture",
    "ReleaseGroupParserFixture",
    "SingleEpisodeParserFixture", "AbsoluteEpisodeNumberParserFixture", "AnimeMetadataParserFixture",
    "MultiEpisodeParserFixture", "SeasonParserFixture", "DailyEpisodeParserFixture",
    "MiniSeriesEpisodeParserFixture", "UnicodeReleaseParserFixture", "ParserFixture",
    "NormalizeSeriesTitleFixture", "NormalizeEpisodeTitleFixture", "CrapParserFixture", "HashedReleaseFixture",
    "SceneCheckerFixture", "IsPossibleSpecialEpisodeFixture", "PathParserFixture", "UrlFixture",
    "SeriesTitleInfoFixture", "ValidateParsedEpisodeInfoFixture",
]
if V5:
    FIXTURES += ["ParsedEpisodeInfoFixture", "SeasonTitleParserFixture", "SubGroupParserFixture"]


DETECTION = {"sourceDetectionSource", "resolutionDetectionSource", "revisionDetectionSource"}


def star(path):
    return ".".join("#" if p == "*" else p for p in path.split(".")) if path else path


def length_path(path):
    """The array-length path for an anyOrder path: the part before the first "*", plus "#"."""
    parts = path.split(".") if path else []
    if "*" in parts:
        parts = parts[:parts.index("*")]
    return ".".join(parts + ["#"])


def to_schema(cid, line, call, inp, expect):
    """Turn a handler's (call, input, expect-with-ops) into the case schema shared with parser-radarr:
    input/inputs/args, expect, contains, compare, not, and "#" paths."""
    case = {"id": cid, "line": line, "call": call}
    if call.endswith("All"):
        # A C# loop over several titles: every input must meet the same expectation.
        case["call"] = call[:-3]
        case["inputs"] = inp
        flat = {}
        for k, v in expect.items():
            assert isinstance(v, list) and all(x == v[0] for x in v), (cid, k)
            nk = k[2:] if k.startswith("*.") else ("" if k == "" else k)
            assert k == "" or k.startswith("*."), (cid, k)
            flat[nk] = v[0]
        expect = flat
    elif isinstance(inp, dict):
        args = dict(inp)
        case["input"] = args.pop("template", "")
        case["args"] = args
    else:
        case["input"] = inp
    if case["call"] == "parseQuality" and DETECTION & set(expect):
        case["call"] = "parseQualityDetection"
    out, contains, nots = {}, {}, {}
    for key, v in expect.items():
        path, _, op = key.partition("|")
        op = op.split("#")[0]
        if op == "":
            out[star(path)] = v
        elif op == "count":
            out[star(path) + ".#" if path else "#"] = v
        elif op == "contains":
            contains.setdefault(star(path), []).append(v)
        elif op == "anyOrder":
            contains.setdefault(star(path), []).extend(v)
            out[length_path(path)] = len(v)
        elif op == "not":
            nots[star(path)] = v
        elif op == "cleanEquals":
            case["compare"] = "cleanSeriesTitle"
            out[star(path)] = v
        else:
            raise ValueError("unknown op %s in %s" % (op, cid))
    case["expect"] = out
    if contains:
        case["contains"] = contains
    if nots:
        case["not"] = nots
    return case


def main():
    H = handler_table()
    kf_path = os.path.join(HERE, KNOWN)
    known = {}
    if os.path.exists(kf_path):
        with open(kf_path, encoding="utf-8") as f:
            known = json.load(f)
    os.makedirs(OUT, exist_ok=True)
    total = 0
    unhandled = set()
    for fx in FIXTURES:
        cases_raw, defs, lines = read_fixture(fx)
        out = []
        counters = {}
        for method, line, args in cases_raw:
            h = H.get((fx, method))
            if h is None:
                unhandled.add((fx, method))
                continue
            call, inp, expect = h(args)
            if V5:
                expect, inp = v5_expect(expect), v5_args(inp)
            idx = counters.get(method, 0)
            counters[method] = idx + 1
            out.append(to_schema("%s/%s/%d" % (fx, method, idx), line, call, inp, expect))
        for method, items in plain_tests(lines).get(fx, []):
            line = defs[method]
            for idx, (call, inp, expect) in enumerate(items):
                if V5:
                    expect, inp = v5_expect(expect), v5_args(inp)
                out.append(to_schema("%s/%s/%d" % (fx, method, idx), line, call, inp, expect))
        for c in out:
            if c["id"] in known:
                c["known_failure"] = known[c["id"]]
        out.sort(key=lambda c: (c["line"], c["id"]))
        doc = {"source": "Sonarr src/NzbDrone.Core.Test/ParserTests/%s.cs @ %s" % (fx, COMMIT), "fixture": fx,
               "cases": out}
        with open(os.path.join(OUT, fx + ".json"), "w", encoding="utf-8") as f:
            json.dump(doc, f, ensure_ascii=False, indent=1)
            f.write("\n")
        total += len(out)
    for fx, m in sorted(unhandled):
        if (fx, m) not in EXCLUDED_METHODS:
            print("unhandled: %s.%s" % (fx, m), file=sys.stderr)
    print("%d cases" % total, file=sys.stderr)


if __name__ == "__main__":
    main()
