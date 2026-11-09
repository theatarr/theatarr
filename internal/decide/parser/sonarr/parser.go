package sonarr

// Port of Sonarr src/NzbDrone.Core/Parser/Parser.cs @ cab419a. The pattern tables keep Sonarr's text, order and
// comments; only the C# verbatim-string quoting ("" for ") is undone.

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/dlclark/regexp2"
	"golang.org/x/text/unicode/norm"
)

var preSubstitutionRegex = []regexReplace{
	// Korean series without season number, replace with S01Exxx and remove airdate
	newRegexReplace(`\.E(\d{2,4})\.\d{6}\.(.*-NEXT)$`, ".S01E$1.$2", optNone),
	// Some Chinese anime releases contain both English and Chinese titles, remove the Chinese title and replace with normal anime pattern
	newRegexReplace(`^\[(?:(?<subgroup>[^\]]+?)(?:[\u4E00-\u9FCC]+)?)\]\[(?<title>[^\]]+?)(?:\s(?<chinesetitle>[\u4E00-\u9FCC][^\]]*?))\]\[(?:(?:[\u4E00-\u9FCC]+?)?(?<episode>\d{1,4})(?:[\u4E00-\u9FCC]+?)?)\]`, "[${subgroup}] ${title} - ${episode} - ", optNone),
	// Chinese LoliHouse/ZERO/Lilith-Raws/Skymoon-Raws/orion origin releases don't use the expected brackets, normalize using brackets
	newRegexReplace(`^\[(?<subgroup>[^\]]*?(?:LoliHouse|ZERO|Lilith-Raws|Skymoon-Raws|orion origin)[^\]]*?)\](?<title>[^\[\]]+?)(?: - (?<episode>[0-9-]+)\s*|\[第?(?<episode>[0-9]+(?:-[0-9]+)?)话?(?:END|完)?\])\[`, "[${subgroup}][${title}][${episode}][", optNone),
	// Most Chinese anime releases contain additional brackets/separators for chinese and non-chinese titles, remove junk first and if it has S0x as season number, convert it to Sx
	newRegexReplace(`^\[(?<subgroup>[^\]]+)\](?:\s?★[^\[ -]+\s?)?\[?(?:(?<chinesetitle>(?=[^\]]*?[\u4E00-\u9FCC])[^\]]*?)(?:\]\[|\s*[_/·]\s*)){0,2}(?<title>[^\[\]]+?)(?:\s(?:S?(?<!\d+)((0)(?<season>\d)|(?<season>[1-9]\d))(?!\d+)))\]?(?:\[\d{4}\])?\[第?(?<episode>[0-9]+(?:-[0-9]+)?)(?:话|集)?(?: ?END|完| ?Fin)?\]`, "[${subgroup}] ${title} S${season} - ${episode} ", optNone),
	// Some Chinese releases don't include a separation between Chinese and English titles within the same bracketed group
	newRegexReplace(`^\[(?<subgroup>[^\]]+)\]\[(?<chinesetitle>(?<![^a-zA-Z0-9])[^a-zA-Z0-9]+)(?<title>[^\]]+?)\](?:\[\d{4}\])?\[第?(?<episode>[0-9]+(?:-[0-9]+)?)(?:话|集)?(?: ?END|完| ?Fin)?\]`, "[${subgroup}] ${title} - ${episode} ", optNone),
	// Most Chinese anime releases contain additional brackets/separators for chinese and non-chinese titles, remove junk and replace with normal anime pattern
	newRegexReplace(`^\[(?<subgroup>[^\]]+)\](?:\s?★[^\[ -]+\s?)?\[?(?:(?<chinesetitle>(?=[^\]]*?[\u4E00-\u9FCC])[^\]]*?)(?:\]\[|\s*[_/·]\s*)){0,2}(?<title>[^\]]+?)\]?(?:\[\d{4}\])?\[第?(?<episode>[0-9]{1,4}(?:-[0-9]{1,4})?)(?:话|集)?(?: ?END|完| ?Fin)?\]`, "[${subgroup}] ${title} - ${episode} ", optNone),
	// Some Chinese anime releases contain both Chinese and English titles, remove the Chinese title first and if it has S0x as season number, convert it to Sx
	newRegexReplace(`^\[(?<subgroup>[^\]]+)\](?:\s)(?:(?<chinesetitle>(?=[^\]]*?[\u4E00-\u9FCC])[^\]]*?)(?:\s/\s))(?<title>[^\[\]]+?)(?:\s(?:S?(?<!\d+)((0)(?<season>\d)|(?<season>[1-9]\d))(?!\d+)))(?:[- ]+)(?<episode>[0-9]+(?:-[0-9]+)?)话?(?:END|完)?`, "[${subgroup}] ${title} S${season} - ${episode} ", optNone),
	// Some Chinese anime releases contain both English and Chinese titles, remove the Chinese title and replace with normal anime pattern
	newRegexReplace(`^\[(?<subgroup>[^\]]+)\](?:\s)(?:(?<title>[^\]]+?)(?:\s/\s))(?<chinesetitle>(?=[^\]]*?[\u4E00-\u9FCC])[^\]]*?)(?:[- ]+)(?<episode>[0-9]+(?:-[0-9]+)?(?![a-z]))话?(?:END|完)?`, "[${subgroup}] ${title} - ${episode} ", optNone),
	// Some Chinese anime releases contain both Chinese and English titles, remove the Chinese title and replace with normal anime pattern
	newRegexReplace(`^\[(?<subgroup>[^\]]+)\](?:\s)(?:(?<chinesetitle>(?=[^\]]*?[\u4E00-\u9FCC])[^\]]*?)(?:\s/\s))(?<title>[^\]]+?)(?:[- ]+)(?<episode>[0-9]+(?:-[0-9]+)?(?![a-z]))话?(?:END|完)?`, "[${subgroup}] ${title} - ${episode} ", optNone),
	// GM-Team releases with lots of square brackets
	newRegexReplace(`^\[(?<subgroup>[^\]]+)\](?:(?<chinesubgroup>\[(?=[^\]]*?[\u4E00-\u9FCC])[^\]]*\])+)\[(?<title>[^\]]+?)\](?<junk>\[[^\]]+\])*\[(?<episode>[0-9]+(?:-[0-9]+)?)( END| Fin)?\]`, "[${subgroup}] ${title} - ${episode} ", optNone),
	// Some Chinese anime releases contain both Chinese and English titles separated by | instead of /, remove the Chinese title and replace with normal anime pattern
	newRegexReplace(`^\[(?<subgroup>[^\]]+)\](?:\s)(?:(?<chinesetitle>(?=[^\]]*?[\u4E00-\u9FCC])[^\]]*?)(?:\s\|\s))(?<title>[^\]]+?)(?:[- ]+)(?<episode>[0-9]+(?:-[0-9]+)?(?![a-z]))话?(?:END|完)?`, "[${subgroup}] ${title} - ${episode} ", optNone),
	// Spanish releases with information in brackets
	newRegexReplace(`^(?<title>.+?(?=[ ._-]\()).+?\((?<year>\d{4})\/(?<info>S[^\/]+)`, "${title} (${year}) - ${info} ", optNone),
}

var reportTitleRegex = []*regexp2.Regexp{
	// Anime - Absolute Episode Number + Title + Season+Episode
	// Todo: This currently breaks series that start with numbers
	// Daily episode with year in series title and air time after date (Plex DVR format)
	re(`^^(?<title>.+?\((?<titleyear>\d{4})\))[-_. ]+(?<airyear>19[4-9]\d|20\d\d)(?<sep>[-_]?)(?<airmonth>0\d|1[0-2])\k<sep>(?<airday>[0-2]\d|3[01])[-_. ]\d{2}[-_. ]\d{2}[-_. ]\d{2}`, optI),
	// Daily episodes without title (2018-10-12, 20181012) (Strict pattern to avoid false matches)
	re(`^(?<airyear>19[6-9]\d|20\d\d)(?<sep>[-_]?)(?<airmonth>0\d|1[0-2])\k<sep>(?<airday>[0-2]\d|3[01])(?!\d)`, optI),
	// Multi-Part episodes without a title (S01E05.S01E06)
	re(`^(?:\W*S(?<season>(?<!\d+)(?:\d{1,2}|\d{4})(?!\d+))(?:e{1,2}(?<episode>\d{1,3}(?!\d+)))+){2,}`, optI),
	// Multi-Part episodes without a title (1x05.1x06)
	re(`^(?:\W*(?<season>(?<!\d+)(?:\d{1,2}|\d{4})(?!\d+))(?:x{1,2}(?<episode>\d{1,3}(?!\d+)))+){2,}`, optI),
	// Episodes without a title, Multi (S01E04E05, 1x04x05, etc)
	re(`^(?:S?(?<season>(?<!\d+)(?:\d{1,2}|\d{4})(?!\d+))(?:(?:[-_]|[ex]){1,2}(?<episode>\d{2,3}(?!\d+))){2,})`, optI),
	// Split episodes (S01E05a, S01E05b, etc)
	re(`^(?<title>.+?)(?:S?(?<season>(?<!\d+)(?:\d{1,2}|\d{4})(?!\d+))(?:(?:[-_ ]?[ex])(?<episode>\d{2,3}(?!\d+))(?<splitepisode>[a-d])(?:[ _.])))`, optI),
	// Episodes without a title, Single (S01E05, 1x05)
	re(`^(?:S?(?<season>(?<!\d+)(?:\d{1,2}|\d{4})(?!\d+))(?:(?:[-_ ]?[ex])(?<episode>\d{2,3}(?!\d+))))`, optI),
	// Anime - [SubGroup] Title Absolute (Season+Episode)
	re(`^(?:\[(?<subgroup>.+?)\](?:_|-|\s|\.)?)(?<title>.+?)[-_. ]+(?<absoluteepisode>(?<!\d+)\d{2,3}(\.\d{1,2})?(?!\d+))(?:[-_. ])+\((?:S(?<season>(?<!\d+)\d{1,2}(?!\d+))(?:(?:[ex]|\W[ex]){1,2}(?<episode>\d{2}(?!\d+))))(?:v\d+)?(?:\)(?!\d+)).*?(?<hash>[(\[]\w{8}[)\]])?$`, optI),
	// Anime - [SubGroup] Title Absolute - Season+Episode
	re(`^(?:\[(?<subgroup>.+?)\](?:_|-|\s|\.)?)(?<title>.+?)[-_. ]+(?<absoluteepisode>(?<!\d+)\d{2,3}(\.\d{1,2})?(?!\d+))(?:[-_. ](?<![()\[!]))+(?:S(?<season>(?<!\d+)\d{1,2}(?!\d+))(?:(?:[ex]|\W[ex]){1,2}(?<episode>\d{2}(?!\d+))))(?:v\d+)?(?:[_. ](?!\d+)).*?(?<hash>[(\[]\w{8}[)\]])?$`, optI),
	// Anime - [SubGroup] Title Season+Episode
	re(`^(?:\[(?<subgroup>.+?)\](?:_|-|\s|\.)?)(?<title>.+?)(?:[-_\W](?<![()\[!]))+(?:S?(?<season>(?<!\d+)\d{1,2}(?!\d+))(?:(?:[ex]|\W[ex]){1,2}(?<episode>\d{2}(?!\d+)))+)(?:v\d+)?(?:[_. ](?!\d+)).*?(?<hash>[(\[]\w{8}[)\]])?$`, optI),
	// Anime - [SubGroup] Title Episode Absolute Episode Number ([SubGroup] Series Title Episode 01)
	re(`^(?:\[(?<subgroup>.+?)\][-_. ]?)(?<title>.+?)[-_. ]+?(?:Episode)(?:[-_. ]+(?<absoluteepisode>(?<!\d+)\d{2,3}(\.\d{1,2})?(?!\d+)))+.*?(?<hash>[(\[]\w{8}[)\]])?$`, optI),
	// Anime - [SubGroup] Title Absolute Episode Number + Season+Episode
	re(`^(?:\[(?<subgroup>.+?)\](?:_|-|\s|\.)?)(?<title>.+?)(?:(?:[-_\W](?<![()\[!]))+(?<absoluteepisode>\d{2,3}(\.\d{1,2})?))+(?:_|-|\s|\.)+(?:S?(?<season>(?<!\d+)\d{1,2}(?!\d+))(?:(?:\-|[ex]|\W[ex]){1,2}(?<episode>\d{2}(?!\d+)))+).*?(?<hash>[(\[]\w{8}[)\]])?(?:$|\.)`, optI),
	// Anime - [SubGroup] Title Season+Episode + Absolute Episode Number
	re(`^(?:\[(?<subgroup>.+?)\](?:_|-|\s|\.)?)(?<title>.+?)(?:[-_\W](?<![()\[!]))+(?:S?(?<season>(?<!\d+)\d{1,2}(?!\d+))(?:(?:\-|[ex]|\W[ex]){1,2}(?<episode>\d{2}(?!\d+)))+)(?:(?:_|-|\s|\.)+(?<absoluteepisode>(?<!\d+)\d{2,3}(\.\d{1,2})?(?!\d+|\-[a-z])))+.*?(?<hash>[(\[]\w{8}[)\]])?$`, optI),
	// Anime - [SubGroup] Title with trailing number Absolute Episode Number - Batch separated with tilde
	re(`^\[(?<subgroup>.+?)\][-_. ]?(?<title>.+?[^-]+?)(?:(?<![-_. ]|\b[0]\d+) - )[-_. ]?(?<absoluteepisode>\d{2,3}(\.\d{1,2})?(?!\d+))\s?~\s?(?<absoluteepisode>\d{2,3}(\.\d{1,2})?(?!\d+))(?:[-_. ]+(?<special>special|ova|ovd))?.*?(?<hash>[(\[]\w{8}[)\]])?(?:$|\.mkv)`, optI),
	// Anime - [SubGroup] Title with season number in brackets Absolute Episode Number
	re(`^\[(?<subgroup>.+?)\][-_. ]?(?<title>[^-]+?)[_. ]+?\(Season[_. ](?<season>\d+)\)[-_. ]+?(?:[-_. ]?(?<absoluteepisode>\d{2,3}(\.\d{1,2})?(?!\d+)))+(?:[-_. ]+(?<special>special|ova|ovd))?.*?(?<hash>[(\[]\w{8}[)\]])?(?:$|\.mkv)`, optI),
	// Anime - [SubGroup] Title with trailing 3-digit number and sub title - Absolute Episode Number
	re(`^\[(?<subgroup>.+?)\][-_. ]?(?<title>[^]]+?)(?:[-_. ]{3}?(?<absoluteepisode>\d{2}(\.\d{1,2})?(?!-?\d+|-[a-z]+)))+(?:[-_. ]+(?<special>special|ova|ovd))?.*?(?<hash>[(\[]\w{8}[)\]])?(?:$|\.mkv)`, optI),
	// Anime - [SubGroup] Title with trailing number Absolute Episode Number
	re(`^\[(?<subgroup>.+?)\][-_. ]?(?<title>[^-]+?)(?:(?<![-_. ]|\b[0]\d+) - )(?:[-_. ]?(?<absoluteepisode>\d{2,3}(\.\d{1,2})?(?!\d+)))+(?:[-_. ]+(?<special>special|ova|ovd))?.*?(?<hash>[(\[]\w{8}[)\]])?(?:$|\.mkv)`, optI),
	// Anime - [SubGroup] Title with trailing number S## (Full season)
	re(`^\[(?<subgroup>.+?)\][-_. ]?(?<title>.+?)[-_. ]+(?:S(?<season>(?<!\d+)(?:\d{1,2}|\d{4})(?![ex]?\d+))).+?(?:$|\.mkv)`, optI),
	// Anime - [SubGroup] Title with trailing number Absolute Episode Number
	re(`^\[(?<subgroup>.+?)\][-_. ]?(?<title>[^-]+?)(?:(?<![-_. ]|\b[0]\d+)[_ ]+)(?:[-_. ]?(?<absoluteepisode>\d{3}(\.\d{1,2})?(?!\d+|-[a-z]+)))+(?:[-_. ]+(?<special>special|ova|ovd))?.*?(?<hash>[(\[]\w{8}[)\]])?(?:$|\.mkv)`, optI),
	// Anime - [SubGroup] Title - Absolute Episode Number
	re(`^\[(?<subgroup>.+?)\][-_. ]?(?<title>.+?)(?:(?<!\b[0]\d+))(?:[. ]-[. ](?<absoluteepisode>\d{2,3}(\.\d{1,2})?(?!\d+|[-])))+(?:[-_. ]+(?<special>special|ova|ovd))?.*?(?<hash>[(\[]\w{8}[)\]])?(?:$|\.mkv)`, optI),
	// Anime - [SubGroup] Title Absolute Episode Number - Absolute Episode Number (batches without full separator between title and absolute episode numbers)
	re(`^\[(?<subgroup>.+?)\][-_. ]?(?<title>.+?)(?:(?<!\b[0]\d+))(?<absoluteepisode>\d{2,3}(\.\d{1,2})?(?!\d+|[-]))[. ]-[. ](?<absoluteepisode>\d{2,3}(\.\d{1,2})?(?!\d+|[-]))(?:[-_. ]+(?<special>special|ova|ovd))?.*?(?<hash>[(\[]\w{8}[)\]])?(?:$|\.mkv)`, optI),
	// Anime - [SubGroup] Title Absolute Episode Number
	re(`^\[(?<subgroup>.+?)\][-_. ]?(?<title>.+?)[-_. ]+\(?(?:[-_. ]?#?(?<absoluteepisode>\d{2,3}(\.\d{1,2})?(?!\d+|-[a-z]+)))+\)?(?:[-_. ]+(?<special>special|ova|ovd))?.*?(?<hash>[(\[]\w{8}[)\]])?(?:$|\.mkv)`, optI),
	// Multi-episode Repeated (S01E05 - S01E06)
	re(`^(?<title>.+?)(?:(?:[-_\W](?<![()\[!]))+S(?<season>(?<!\d+)(?:\d{1,2}|\d{4})(?!\d+))(?:(?:e|[-_. ]e){1,2}(?<episode>\d{1,3}(?!\d+)))+){2,}`, optI),
	// Multi-episode Repeated (1x05 - 1x06)
	re(`^(?<title>.+?)(?:(?:[-_\W](?<![()\[!]))+(?<season>(?<!\d+)(?:\d{1,2}|\d{4})(?!\d+))(?:x{1,2}(?<episode>\d{1,3}(?!\d+)))+){2,}`, optI),
	// Single episodes with a title (S01E05, 1x05, etc) followed by ".5 [SP]"
	re(`^(?<title>.+?)(?:(?:[-_\W](?<![()\[!]))+S?(?<season>(?<!\d+)(?:\d{1,2})(?!\d+))(?:[ex]|\W[ex]|_){1,2}(?<episode>\d{2,3}(?!\d+|(?:[ex]|\W[ex]|_|-){1,2}\d+))(?<special>\.5[ .]\[SP\]))`, optI),
	// Single episodes with a title (S01E05, 1x05, etc) and trailing info in slashes
	re(`^(?<title>.+?)(?:(?:[-_\W](?<![()\[!]))+S?(?<season>(?<!\d+)(?:\d{1,2})(?!\d+))(?:[ex]|\W[ex]|_){1,2}(?<episode>\d{2,3}(?!\d+|(?:[ex]|\W[ex]|_|-){1,2}\d+))).+?(?:\[.+?\])(?!\\)`, optI),
	// Anime - Title Season EpisodeNumber + Absolute Episode Number [SubGroup]
	re(`^(?<title>.+?)(?:[-_\W](?<![()\[!]))+(?:S?(?<season>(?<!\d+)\d{1,2}(?!\d+))(?:(?:[ex]|\W[ex]|-){1,2}(?<episode>(?<!\d+)\d{2}(?!\d+)))+)[-_. (]+?(?:[-_. ]?(?<absoluteepisode>(?<!\d+)\d{3}(\.\d{1,2})?(?!\d+|[pi])))+.+?\[(?<subgroup>.+?)\](?:$|\.mkv)`, optI),
	// Multi-Episode with a title (S01E05E06, S01E05-06, S01E05 E06, etc) and trailing info in slashes
	re(`^(?<title>.+?)(?:(?:[-_\W](?<![()\[!]))+S?(?<season>(?<!\d+)(?:\d{1,2})(?!\d+))(?:[ex]|\W[ex]|_){1,2}(?<episode>\d{2,3}(?!\d+))(?:(?:\-|[ex]|\W[ex]|_){1,2}(?<episode>\d{2,3}(?!\d+)))+).+?(?:\[.+?\])(?!\\)`, optI),
	// Anime - Title Absolute Episode Number [SubGroup] [Hash]? (Series Title Episode 99-100 [RlsGroup] [ABCD1234])
	re(`^(?<title>.+?)[-_. ]Episode(?:[-_. ]+(?<absoluteepisode>\d{2,3}(\.\d{1,2})?(?!\d+)))+(?:.+?)\[(?<subgroup>.+?)\].*?(?<hash>[(\[]\w{8}[)\]])?$`, optI),
	// Anime - Title Absolute Episode Number [SubGroup] [Hash]
	re(`^(?<title>.+?)(?:(?:_|-|\s|\.)+(?<absoluteepisode>\d{3}(\.\d{1,2})(?!\d+)))+(?:.+?)\[(?<subgroup>.+?)\].*?(?<hash>[(\[]\w{8}[)\]])?$`, optI),
	// Anime - Title Absolute Episode Number (Year) [SubGroup]
	re(`^(?<title>.+?)[-_. ]+(?<absoluteepisode>(?<!\d+)\d{2}(?!\d+))[-_. ](\(\d{4}\))[-_. ]\[(?<subgroup>.+?)\]`, optI),
	// Anime - Title with trailing number, Absolute Episode Number and hash
	re(`^(?<title>[^-]+?)(?:(?<![-_. ]|\b[0]\d+) - )(?:[-_. ]?(?<absoluteepisode>\d{2,3}(\.\d{1,2})?(?!\d+)))+(?:[-_. ]+(?<special>special|ova|ovd))?.*?(?<hash>[(\[]\w{8}[)\]])(?:$|\.mkv)`, optI),
	// Anime - Title Absolute Episode Number [Hash]
	re(`^(?<title>.+?)(?:(?:_|-|\s|\.)+(?<absoluteepisode>\d{2,3}(\.\d{1,2})?(?!\d+)))+(?:[-_. ]+(?<special>special|ova|ovd))?[-_. ]+.*?(?<hash>[(\[]\w{8}[)\]])$`, optI),
	// Episodes with airdate AND season/episode number, capture season/episode only
	re(`^(?<title>.+?)?\W*(?<airdate>\d{4}\W+[0-1][0-9]\W+[0-3][0-9])(?!\W+[0-3][0-9])[-_. ](?:s?(?<season>(?<!\d+)(?:\d{1,2})(?!\d+)))(?:[ex](?<episode>(?<!\d+)(?:\d{1,3})(?!\d+)))`, optI),
	// Episodes with airdate AND season/episode number
	re(`^(?<title>.+?)?\W*(?<airyear>\d{4})\W+(?<airmonth>[0-1][0-9])\W+(?<airday>[0-3][0-9])(?!\W+[0-3][0-9]).+?(?:s?(?<season>(?<!\d+)(?:\d{1,2})(?!\d+)))(?:[ex](?<episode>(?<!\d+)(?:\d{1,3})(?!\d+)))`, optI),
	// Episodes with absolute episode number AND airdate (TJET Wrestling)
	re(`^(?<title>.+?)?[-_. ](?:e\d{2,3}(?!\d+))[-_. ](?<airyear>\d{4})\W+(?<airmonth>[0-1][0-9])\W+(?<airday>[0-3][0-9])(?!\W+[0-3][0-9]).+?(?:\[[a-z]+\])`, optI),
	// Single or multi episode releases with multiple titles, then season and episode numbers after the last title. (Title1 / Title2 / ... / S1E1-2 of 6)
	re(`^((?<title>.*?)[ ._]\/[ ._])+\(?S(?<season>(?<!\d+)\d{1,2}(?!\d+))(?:\W|_)?E?[ ._]?(?<episode>(?<!\d+)\d{1,2}(?!\d+))(?:-(?<episode>(?<!\d+)\d{1,2}(?!\d+)))?(?:[ ._]of[ ._](?<episodecount>\d{1,2}))?\)?[ ._][\(\[]`, optI),
	// Multi-episode with title (S01E99-100, S01E05-06)
	re(`^(?<title>.+?)(?:[-_\W](?<![()\[!]))+S(?<season>(?<!\d+)(?:\d{1,2})(?!\d+))E(?<episode>\d{2,3}(?!\d+))(?:-(?<episode>\d{2,3}(?!\d+)))+(?:[-_. ]|$)`, optI),
	// Multi-episode with title (S01E05-06, S01E05-6)
	re(`^(?<title>.+?)(?:[-_\W](?<![()\[!]))+S(?<season>(?<!\d+)(?:\d{1,2})(?!\d+))E(?<episode>\d{1,2}(?!\d+))(?:-(?<episode>\d{1,2}(?!\d+)))+(?:[-_. ]|$)`, optI),
	// Episodes with a title, Single episodes (S01E05, 1x05, etc) & Multi-episode (S01E05E06, S01E05-06, S01E05 E06, etc)
	re(`^(?<title>.+?)(?:(?:[-_\W](?<![()\[!]))+S?(?<season>(?<!\d+)(?:\d{1,2})(?!\d+))(?:[ex]|\W[ex]){1,2}(?<episode>\d{2,3}(?!\d+))(?:(?:\-|[ex]|\W[ex]|_){1,2}(?<episode>\d{2,3}(?!\d+)))*)(?:[-_. ]|$)`, optI),
	// Episodes with a title, 4 digit season number, Single episodes (S2016E05, etc) & Multi-episode (S2016E05E06, S2016E05-06, S2016E05 E06, etc)
	re(`^(?<title>.+?)(?:(?:[-_\W](?<![()\[!]))+S(?<season>(?<!\d+)(?:\d{4})(?!\d+))(?:e|\We|_){1,2}(?<episode>\d{2,4}(?!\d+))(?:(?:\-|e|\We|_){1,2}(?<episode>\d{2,3}(?!\d+)))*)\W?(?!\\)`, optI),
	// Episodes with a title, 4 digit season number, Single episodes (2016x05, etc) & Multi-episode (2016x05x06, 2016x05-06, 2016x05 x06, etc)
	re(`^(?<title>.+?)(?:(?:[-_\W](?<![()\[!]))+(?<season>(?<!\d+)(?:\d{4})(?!\d+))(?:x|\Wx){1,2}(?<episode>\d{2,4}(?!\d+))(?:(?:\-|x|\Wx|_){1,2}(?<episode>\d{2,3}(?!\d+)))*)\W?(?!\\)`, optI),
	// Episodes with a title, Single episodes (s01.05)
	re(`^(?<title>.+?)(?:[-_\W](?<![()\[!]))+S(?<season>(?<!\d+)(?:\d{2})(?!\d+))(?:\.)(?<episode>\d{2,3}(?!\d+))(?:[-_. ]|$)`, optI),
	// Multi-season pack
	re(`^(?<title>.+?)(Complete Series)?[-_. ]+(?:S|(?:Season|Saison|Series|Stagione)[_. ])(?<season>(?<!\d+)(?:\d{1,2})(?!\d+))(?:[-_. ]{1}|[-_. ]{3})(?:S|(?:Season|Saison|Series|Stagione)[_. ])?(?<season>(?<!\d+)(?:\d{1,2})(?!\d+))`, optI),
	// Partial season pack
	re(`^(?<title>.+?)(?:\W+S(?<season>(?<!\d+)(?:\d{1,2})(?!\d+))\W+(?:(?:(?:Part|Vol)\W?|(?<!\d+\W+)e|p)(?<seasonpart>\d{1,2}(?!\d+)))+)`, optI),
	// Anime - 4 digit absolute episode number
	re(`^(?:\[(?<subgroup>.+?)\][-_. ]?)(?<title>.+?)[-_. ]+?(?<absoluteepisode>\d{4}(\.\d{1,2})?(?!\d+))`, optI),
	// Anime - Title 4-digit Absolute Episode Number [SubGroup]
	re(`^(?<title>.+?)[-_. ]+(?<absoluteepisode>(?<!\d+)\d{4}(?!\d+))[-_. ]\[(?<subgroup>.+?)\]`, optI),
	// Mini-Series with year in title, treated as season 1, episodes are labelled as Part01, Part 01, Part.1
	re(`^(?<title>.+?\d{4})(?:\W+(?:(?:Part\W?|e)(?<episode>\d{1,2}(?!\d+)))+)`, optI),
	// Mini-Series, treated as season 1, multi episodes are labelled as E1-E2
	re(`^(?<title>.+?)(?:[-._ ][e])(?<episode>\d{2,3}(?!\d+))(?:(?:\-?[e])(?<episode>\d{2,3}(?!\d+)))+`, optI),
	// Episodes with airdate and part (2018.04.28.Part.2)
	re(`^(?<title>.+?)?\W*(?<airyear>\d{4})[-_. ]+(?<airmonth>[0-1][0-9])[-_. ]+(?<airday>[0-3][0-9])(?![-_. ]+[0-3][0-9])[-_. ]+Part[-_. ]?(?<part>[1-9])`, optI),
	// Mini-Series, treated as season 1, episodes are labelled as Part01, Part 01, Part.1
	re(`^(?<title>.+?)(?:\W+(?:(?:(?<!\()Part\W?|(?<!\d+\W+)e)(?<episode>\d{1,2}(?!\d+|\))))+)`, optI),
	// Mini-Series, treated as season 1, episodes are labelled as Part One/Two/Three/...Nine, Part.One, Part_One
	re(`^(?<title>.+?)(?:\W+(?:Part[-._ ](?<episode>One|Two|Three|Four|Five|Six|Seven|Eight|Nine)(?>[-._ ])))`, optI),
	// Mini-Series, treated as season 1, episodes are labelled as XofY
	re(`^(?<title>.+?)(?:\W+(?:(?<episode>(?<!\d+)\d{1,2}(?!\d+))of\d+)+)`, optI),
	// Supports Season 01 Episode 03
	re(`(?:.*(?:\"|^))(?<title>.*?)(?:[-_\W](?<![()\[]))+(?:\W?Season\W?)(?<season>(?<!\d+)\d{1,2}(?!\d+))(?:\W|_)+(?:Episode\W)(?:[-_. ]?(?<episode>(?<!\d+)\d{1,2}(?!\d+)))+`, optI),
	// Multi-episode with episodes in square brackets (Series Title [S01E11E12] or Series Title [S01E11-12])
	re(`(?:.*(?:^))(?<title>.*?)[-._ ]+\[S(?<season>(?<!\d+)\d{2}(?!\d+))(?:[E-]{1,2}(?<episode>(?<!\d+)\d{2}(?!\d+)))+\]`, optI),
	// Multi-episode with episodes in brackets (Series Title (S01E11E12) or Series Title (S01E11-12))
	re(`(?:.*(?:^))(?<title>.*?)[-._ ]+\(S(?<season>(?<!\d+)\d{2}(?!\d+))(?:[E-]{1,2}(?<episode>(?<!\d+)\d{2}(?!\d+)))+\)`, optI),
	// Multi-episode release with no space between series title and season (S01E11E12)
	re(`(?:.*(?:^))(?<title>.*?)S(?<season>(?<!\d+)\d{2}(?!\d+))(?:E(?<episode>(?<!\d+)\d{2}(?!\d+)))+`, optI),
	// Multi-episode with single episode numbers (S6.E1-E2, S6.E1E2, S6E1E2, etc)
	re(`^(?<title>.+?)[-_. ]S(?<season>(?<!\d+)(?:\d{1,2}|\d{4})(?!\d+))(?:[-_. ]?[ex]?(?<episode>(?<!\d+)\d{1,2}(?!\d+)))+`, optI),
	// Single or multi episode releases with multiple titles, each followed by season and episode numbers in brackets
	re(`^(?<title>.*?)[ ._]\(S(?<season>(?<!\d+)\d{1,2}(?!\d+))(?:\W|_)?E?[ ._]?(?<episode>(?<!\d+)\d{1,2}(?!\d+))(?:-(?<episode>(?<!\d+)\d{1,2}(?!\d+)))?\)(?:[ ._]\/[ ._])(?<title>.*?)[ ._]\(`, optI),
	// Single episode season or episode S1E1 or S1-E1 or S1.Ep1 or S01.Ep.01
	re(`(?:.*(?:\"|^))(?<title>.*?)(?:\W?|_)S(?<season>(?<!\d+)\d{1,2}(?!\d+))(?:\W|_)?Ep?[ ._]?(?<episode>(?<!\d+)\d{1,2}(?!\d+))`, optI),
	// 3 digit season S010E05
	re(`(?:.*(?:\"|^))(?<title>.*?)(?:\W?|_)S(?<season>(?<!\d+)\d{3}(?!\d+))(?:\W|_)?E(?<episode>(?<!\d+)\d{1,2}(?!\d+))`, optI),
	// 5 digit episode number with a title
	re(`^(?:(?<title>.+?)(?:_|-|\s|\.)+)(?:S?(?<season>(?<!\d+)\d{1,2}(?!\d+)))(?:(?:\-|[ex]|\W[ex]|_){1,2}(?<episode>(?<!\d+)\d{5}(?!\d+)))`, optI),
	// 5 digit multi-episode with a title
	re(`^(?:(?<title>.+?)(?:_|-|\s|\.)+)(?:S?(?<season>(?<!\d+)\d{1,2}(?!\d+)))(?:(?:[-_. ]{1,3}ep){1,2}(?<episode>(?<!\d+)\d{5}(?!\d+)))+`, optI),
	// Separated season and episode numbers S01 - E01
	re(`^(?<title>.+?)(?:_|-|\s|\.)+S(?<season>\d{2}(?!\d+))(\W-\W)E(?<episode>(?<!\d+)\d{2}(?!\d+))(?!\\)`, optI),
	// Season and episode numbers in square brackets (single and multi-episode)
	// Series Title - [02x01] - Episode 1
	// Series Title - [02x01x02] - Episode 1
	re(`^(?<title>.+?)?(?:[-_\W](?<![()\[!]))+\[(?:s)?(?<season>(?<!\d+)\d{1,2})(?:(?:[ex])(?<episode>\d{2}))(?:(?:[-ex]){1,2}(?<episode>\d{2}))*\].+?(?:\.|$)`, optI),
	// Anime - Title with season number - Absolute Episode Number (Title S01 - EP14)
	re(`^(?<title>.+?S\d{1,2})[-_. ]{3,}(?:EP)?(?<absoluteepisode>\d{2,3}(\.\d{1,2})?(?!\d+|[-]))`, optI),
	// Anime - French titles with single episode numbers, with or without leading sub group ([RlsGroup] Title - Episode 1)
	re(`^(?:\[(?<subgroup>.+?)\][-_. ]?)?(?<title>.+?)[-_. ]+?(?:Episode[-_. ]+?)(?<absoluteepisode>\d{1}(\.\d{1,2})?(?!\d+))`, optI),
	// Anime - Absolute episode number in square brackets
	re(`^(?:\[(?<subgroup>.+?)\][-_. ]?)(?<title>.+?)[-_. ]+?\[(?<absoluteepisode>\d{2,3}(\.\d{1,2})?(?!\d+))\]`, optI),
	// Japanese variety shows with leading date
	re(`^(?<airyear>\d{2})(?<airmonth>[0-1][0-9])(?<airday>[0-3][0-9])(?![-_. ]+[0-3][0-9])[-_. ](?<title>.+?)[-_. ](?:Season[-_. ]?(?<season>\d{1,2})[-_. ])?(?:ep|#)(?<episode>\d{2,3})`, optI),
	// Season only releases followed by year
	re(`^(?<title>.+?)[-_. ]+?(?:S|Season|Saison|Series|Stagione)[-_. ]?(?<season>\d{1,2}(?=[-_. ]\d{4}[-_. ]+))(?<extras>EXTRAS|SUBPACK)?(?!\\)`, optI),
	// Season only releases
	re(`^(?<title>.+?)[-_. ]+?(?:S|Season|Saison|Series|Stagione)[-_. ]?(?<season>\d{1,2}(?![-_. ]?\d+))(?:[-_. ]|$)+(?<extras>EXTRAS|SUBPACK)?(?!\\)`, optI),
	// 4 digit season only releases
	re(`^(?<title>.+?)[-_. ]+?(?:S|Season|Saison|Series|Stagione)[-_. ]?(?<season>\d{4}(?![-_. ]?\d+))(\W+|_|$)(?<extras>EXTRAS|SUBPACK)?(?!\\)`, optI),
	// Spanish tracker releases
	re(`^(?<title>.+?)(?:(?:[-_. ]+?Temporada.+?|\[.+?\])\[Cap)(?:[-_. ]+(?<season>(?<!\d+)\d{1,2})(?<episode>(?<!e|x)(?:[1-9][0-9]|[0][1-9])))+(?:\])`, optI),
	// Supports 103/113 naming
	re(`^(?<title>.+?)?(?:(?:[_.-](?<![()\[!]))+(?<season>(?<!\d+)[1-9])(?<episode>[1-9][0-9]|[0][1-9])(?![a-z]|\d+))+(?:[_.]|$)`, optI),
	// 4 digit episode number
	// Episodes without a title, Single (S01E05, 1x05) AND Multi (S01E04E05, 1x04x05, etc)
	re(`^(?:S?(?<season>(?<!\d+)\d{1,2}(?!\d+))(?:(?:\-|[ex]|\W[ex]|_){1,2}(?<episode>\d{4}(?!\d+|i|p)))+)(\W+|_|$)(?!\\)`, optI),
	// 4 digit episode number
	// Episodes with a title, Single episodes (S01E05, 1x05, etc) & Multi-episode (S01E05E06, S01E05-06, S01E05 E06, etc)
	re(`^(?<title>.+?)(?:(?:[-_\W](?<![()\[!]|\d{1,2}-))+S?(?<season>(?<!\d+)\d{1,2}(?!\d+))(?:(?:\-|[ex]|\W[ex]|_){1,2}(?<episode>\d{4}(?!\d+|i|p)))+)\W?(?!\\)`, optI),
	// Episodes with airdate (2018.04.28)
	re(`^(?<title>.+?)?\W*(?<airyear>\d{4})[-_. ]+(?<airmonth>[0-1][0-9])[-_. ]+(?<airday>[0-3][0-9])(?![-_. ]+[0-3][0-9])`, optI),
	// Turkish tracker releases (01 BLM, 3. Blm, 04.Bolum, etc)
	re(`^(?<title>.+?)[_. ](?<absoluteepisode>\d{1,4})(?:[_. ]+)(?:BLM|B[oö]l[uü]m)`, optI),
	// Episodes with airdate (04.28.2018)
	re(`^(?<title>.+?)?\W*(?<ambiguousairmonth>[0-1][0-9])[-_. ]+(?<ambiguousairday>[0-3][0-9])[-_. ]+(?<airyear>\d{4})(?!\d+)`, optI),
	// Episodes with airdate (28.04.2018)
	re(`^(?<title>.+?)?\W*(?<ambiguousairday>[0-3][0-9])[-_. ]+(?<ambiguousairmonth>[0-1][0-9])[-_. ]+(?<airyear>\d{4})(?!\d+)`, optI),
	// Episodes with airdate (20180428)
	re(`^(?<title>.+?)?\W*(?<!\d+)(?<airyear>\d{4})(?<airmonth>[0-1][0-9])(?<airday>[0-3][0-9])(?!\d+)`, optI),
	// Anime - Title Absolute Episode Number (E195 or E1206)
	re(`^(?:\[(?<subgroup>.+?)\][-_. ]?)?(?<title>.+?)(?:(?:_|-|\s|\.)+(?:e|ep)(?<absoluteepisode>(\d{3}|\d{4})(\.\d{1,2})?))+[-_. ].*?(?<hash>[(\[]\w{8}[)\]])?$`, optI),
	// Supports 1103/1113 naming
	re(`^(?<title>.+?)?(?:(?:[-_. ](?<![()\[!]))*(?<!\d{1,2}-)(?<season>(?<!\d+|\(|\[|e|x)\d{2})(?<episode>(?<!e|x)(?:[1-9][0-9]|[0][1-9])(?!p|i|\d+|\)|\]|\W\d+|\W(?:e|ep|x)\d+)))+([-_. ]+|$)(?!\\)`, optI),
	// Dutch/Flemish release titles
	re(`^(?<title>.+?)[-_. ](?:Se\.(?<season>(?<!\d+)(?:\d{1,2}|\d{4})(?!\d+))(?:(?:[-_ ]?afl\.)(?<episode>\d{1,3}(?!\d+))(?:(?:[-]|[-_ ]en[-_ ])(?<episode>\d{1,3}(?!\d+)))*))`, optI),
	// Episodes with single digit episode number (S01E1, S01E5E6, etc)
	re(`^(?<title>.*?)(?:(?:[-_\W](?<![()\[!]))+S?(?<season>(?<!\d+)\d{1,2}(?!\d+))(?:(?:\-|[ex]){1,2}(?<episode>\d{1}))+)+(\W+|_|$)(?!\\)`, optI),
	// iTunes Season 1\05 Title (Quality).ext
	re(`^(?:Season(?:_|-|\s|\.)(?<season>(?<!\d+)\d{1,2}(?!\d+)))(?:_|-|\s|\.)(?<episode>(?<!\d+)\d{1,2}(?!\d+))`, optI),
	// iTunes 1-05 Title (Quality).ext
	re(`^(?:(?<season>(?<!\d+)(?:\d{1,2})(?!\d+))(?:-(?<episode>\d{2,3}(?!\d+))))`, optI),
	// Anime Range - Title Absolute Episode Number (ep01-12)
	re(`^(?:\[(?<subgroup>.+?)\][-_. ]?)?(?<title>.+?)(?:_|\s|\.)+(?:e|ep)(?<absoluteepisode>\d{2,3}(\.\d{1,2})?)-(?<absoluteepisode>(?<!\d+)\d{1,2}(\.\d{1,2})?(?!\d+|-)).*?(?<hash>[(\[]\w{8}[)\]])?$`, optI),
	// Anime - Title Absolute Episode Number (e66)
	re(`^(?:\[(?<subgroup>.+?)\][-_. ]?)?(?<title>.+?)(?:(?:_|-|\s|\.)+(?:e|ep)(?<absoluteepisode>\d{2,4}(\.\d{1,2})?))+[-_. ].*?(?<hash>[(\[]\w{8}[)\]])?$`, optI),
	// Anime - Title Episode Absolute Episode Number (Series Title Episode 01)
	re(`^(?<title>.+?)[-_. ](?:Episode)(?:[-_. ]+(?<absoluteepisode>(?<!\d+)\d{2,3}(\.\d{1,2})?(?!\d+)))+.*?(?<hash>[(\[]\w{8}[)\]])?$`, optI),
	// Anime Range - Title Absolute Episode Number (1 or 2 digit absolute episode numbers in a range, 1-10)
	re(`^(?:\[(?<subgroup>.+?)\][-_. ]?)?(?<title>.+?)[_. ]+(?<absoluteepisode>(?<!\d+)\d{1,2}(\.\d{1,2})?(?!\d+))-(?<absoluteepisode>(?<!\d+)\d{1,2}(\.\d{1,2})?(?!\d+|-)).*?(?<hash>[(\[]\w{8}[)\]])?$`, optI),
	// Anime - Title Episode/Episodio Absolute Episode Number
	re(`^(?:\[(?<subgroup>.+?)\][-_. ]?)?(?<title>.+?)[-_. ]+(?:Episode|Episodio)(?:[-_. ]+(?<absoluteepisode>(?<!\d+)\d{2,4}(\.\d{1,2})?(?!\d+|[ip])))+.*?(?<hash>[(\[]\w{8}[)\]])?$`, optI),
	// Anime - Title [Absolute Episode Number] from AniLibriaTV
	re(`^(?:\[(?<subgroup>.+?)\][-_. ]?)?(?<title>.+?)(?:[-_. ]\[)(?:(?:-?)(?<absoluteepisode>(?<!\d+)\d{2,3}(\.\d{1,2})?(?!\d+|[ip])))+(?:\][-_. ]).*?(?<hash>[(\[]\w{8}[)\]])?$`, optI),
	// Anime - Title Absolute Episode Number
	re(`^(?:\[(?<subgroup>.+?)\][-_. ]?)?(?<title>.+?)(?:[-_. ]+(?<absoluteepisode>(?<!\d+)\d{2,4}(\.\d{1,2})?(?!\d+|[ip])))+.*?(?<hash>[(\[]\w{8}[)\]])?$`, optI),
	// Anime - Title {Absolute Episode Number}
	re(`^(?:\[(?<subgroup>.+?)\][-_. ]?)?(?<title>.+?)(?:(?:[-_\W](?<![()\[!]))+(?<absoluteepisode>(?<!\d+)\d{2,3}(\.\d{1,2})?(?!\d+|[ip])))+.*?(?<hash>[(\[]\w{8}[)\]])?$`, optI),
	// Extant, terrible multi-episode naming (extant.10708.hdtv-lol.mp4)
	re(`^(?<title>.+?)[-_. ](?<season>[0]?\d?)(?:(?<episode>\d{2}){2}(?!\d+))[-_. ]`, optI),
	// Season only releases for poorly named anime
	re(`^(?:\[(?<subgroup>.+?)\][-_. ])?(?<title>.+?)[-_. ]+?[\[(](?:S|Season|Saison|Series|Stagione)[-_. ]?(?<season>\d{1,2}(?![-_. ]?\d+))(?:[-_. )\]]|$)+(?<extras>EXTRAS|SUBPACK)?(?!\\)`, optI),
	// Episodes without a title, Single episode numbers (S1E1, 1x1)
	re(`^(?:S?(?<season>(?<!\d+)(?:\d{1,2}|\d{4})(?!\d+))(?:(?:[-_ ]?[ex])(?<episode>\d{1}(?!\d+))))`, optI),
}

var specialEpisodeTitleRegex = []*regexp2.Regexp{
	re(`\.S\d+E00\.(?<episodetitle>.+?)(?:\.(?:720p|1080p|2160p|HDTV|WEB|WEBRip|WEB-DL)\.|$)`, optI),
	re(`\.S\d+\.Special\.(?<episodetitle>.+?)(?:\.(?:720p|1080p|2160p|HDTV|WEB|WEBRip|WEB-DL)\.|$)`, optI),
}

var rejectHashedReleasesRegexes = []*regexp2.Regexp{
	// Generic match for md5 and mixed-case hashes.
	re(`^[0-9a-zA-Z]{32}`, optNone),

	// Generic match for shorter lower-case hashes.
	re(`^[a-z0-9]{24}$`, optNone),

	// Format seen on some NZBGeek releases
	// Be very strict with these coz they are very close to the valid 101 ep numbering.
	re(`^[A-Z]{11}\d{3}$`, optNone),
	re(`^[a-z]{12}\d{3}$`, optNone),

	// Backup filename (Unknown origins)
	re(`^Backup_\d{5,}S\d{2}-\d{2}$`, optNone),

	// 123 - Started appearing December 2014
	re(`^123$`, optNone),

	// abc - Started appearing January 2015
	re(`^abc$`, optI),

	// abc - Started appearing 2020
	re(`^abc[-_. ]xyz`, optI),

	// b00bs - Started appearing January 2015
	re(`^b00bs$`, optI),

	// 170424_26 - Started appearing August 2018
	re(`^\d{6}_\d{2}$`, optNone),

	// additional Generic match for mixed-case hashes. - Started appearing Dec 2020
	re(`^[0-9a-zA-Z]{30}`, optNone),

	// additional Generic match for mixed-case hashes. - Started appearing Jan 2021
	re(`^[0-9a-zA-Z]{26}`, optNone),

	// additional Generic match for mixed-case hashes. - Started appearing Jan 2021
	re(`^[0-9a-zA-Z]{39}`, optNone),

	// additional Generic match for mixed-case hashes. - Started appearing Jan 2021
	re(`^[0-9a-zA-Z]{24}`, optNone),
}

var seasonFolderRegexes = []*regexp2.Regexp{
	re(`^(Season[ ._-]*\d+|Specials)$`, optNone),
}

var (
	// Regex to detect whether the title was reversed.
	reversedTitleRegex = re(`(?:^|[-._ ])(p027|p0801|\d{2,3}E\d{2}S)[-._ ]`, optNone)

	normalizeRegex = newRegexReplace(`((?:\b|_)(?<!^)([aà](?!$)|an|the|and|or|of)(?!$)(?:\b|_))|\W|_`, "", optI)

	percentRegex = re(`(?<=\b\d+)%`, optNone)

	fileExtensionRegex = re(`\.[a-z0-9]{2,4}$`, optI)

	simpleTitleRegex = newRegexReplace(`(?:(480|540|576|720|1080|2160)[ip]|[xh][\W_]?26[45]|DD\W?5\W1|[<>?*]|848x480|1280x720|1920x1080|3840x2160|4096x2160|(?<![a-f0-9])(8|10)(b(?![a-z0-9])|bit)|10-bit)\s*?`, "", optI)

	// Valid TLDs http://data.iana.org/TLD/tlds-alpha-by-domain.txt

	websitePrefixRegex = newRegexReplace(`^(?:(?:\[|\()\s*)?(?:www\.)?[-a-z0-9-]{1,256}\.(?<!Naruto-Kun\.)(?:[a-z]{2,6}\.[a-z]{2,6}|xn--[a-z0-9-]{4,}|[a-z]{2,})\b(?:\s*(?:\]|\))|[ -]{2,})[ -]*`, "", optI)

	websitePostfixRegex = newRegexReplace(`(?:\[\s*)?(?:www\.)?[-a-z0-9-]{1,256}\.(?:xn--[a-z0-9-]{4,}|[a-z]{2,6})\b(?:\s*\])$`, "", optI)

	sixDigitAirDateRegex = re(`(?<=[_.-])(?<airdate>(?<!\d)(?<airyear>[1-9]\d{1})(?<airmonth>[0-1][0-9])(?<airday>[0-3][0-9]))(?=[_.-])`, optI)

	cleanReleaseGroupRegex = newRegexReplace(`^(.*?[-._ ](S\d+E\d+)[-._ ])|(-(RP|1|NZBGeek|Obfuscated|Scrambled|sample|Pre|postbot|xpost|Rakuv[a-z0-9]*|WhiteRev|BUYMORE|AsRequested|AlternativeToRequested|GEROV|Z0iDS3N|Chamele0n|4P|4Planet|AlteZachen|RePACKPOST))+$`, "", optI)

	cleanTorrentSuffixRegex = newRegexReplace(`\[(?:ettv|rartv|rarbg|cttv|publichd)\]$`, "", optI)

	cleanQualityBracketsRegex = re(`\[[a-z0-9 ._-]+\]$`, optI)

	releaseGroupRegex = re(`-(?<releasegroup>[a-z0-9]+(?<part2>-[a-z0-9]+)?(?!.+?(?:480p|576p|720p|1080p|2160p)))(?<!(?:WEB-DL|Blu-Ray|480p|576p|720p|1080p|2160p|DTS-HD|DTS-X|DTS-MA|DTS-ES|-ES|-EN|-CAT|[ ._]\d{4}-\d{2}|-\d{2})(?:\k<part2>)?)(?:\b|[-._ ]|$)|[-._ ]\[(?<releasegroup>[a-z0-9]+)\]$`, optI)

	invalidReleaseGroupRegex = re(`^([se]\d+|[0-9a-f]{8})$`, optI)

	animeReleaseGroupRegex = re(`^(?:\[(?<subgroup>(?!\s).+?(?<!\s))\](?:_|-|\s|\.)?)`, optI)

	// Handle Exception Release Groups that don't follow -RlsGrp; Manual List
	// name only...be very careful with this last; high chance of false positives
	exceptionReleaseGroupRegexExact = re(`(?<releasegroup>(?:D\-Z0N3|Fight-BB|VARYG|E\.N\.D|KRaLiMaRKo|BluDragon|DarQ|KCRT|BEN[_. ]THE[_. ]MEN)\b)`, optI)

	// groups whose releases end with RlsGroup) or RlsGroup]
	exceptionReleaseGroupRegex = re(`(?<=[._ \[])(?<releasegroup>(Silence|afm72|Panda|Ghost|MONOLITH|Tigole|Joy|ImE|UTR|t3nzin|Anime Time|Project Angel|Hakata Ramen|HONE|Vyndros|SEV|Garshasp|Kappa|Natty|RCVR|SAMPA|YOGI|r00t|EDGE2020|RZeroX)(?=\]|\)))`, optI)

	yearInTitleRegex = re(`^(?<title>.+?)[-_. ]+?[\(\[]?(?<year>\d{4})[\]\)]?`, optI)

	titleComponentsRegex = re(`^(?:(?<title>.+?) \((?<title>.+?)\)|(?<title>.+?) \| (?<title>.+?)|(?<title>.+?) AKA (?<title>.+?))$`, optI)

	partRegex                = re(`\(\d+\)$`, optNone)
	punctuationRegex         = re(`[^\w\s]`, optNone)
	articleWordRegex         = re(`^(a|an|the)\s`, optI)
	specialEpisodeWordRegex  = re(`\b(part|special|edition|christmas)\b\s?`, optI)
	duplicateSpacesRegex     = re(`\s{2,}`, optNone)
	seasonFolderRegex        = re(`^(?:S|Season|Saison|Series|Stagione)[-_. ]*(?<season>(?<!\d+)\d{1,4}(?!\d+))(?:[_. ]+(?!\d+)|$)`, optI)
	simpleEpisodeNumberRegex = re(`^[ex]?(?<episode>(?<!\d+)\d{1,3}(?!\d+))(?:[ex-](?<episode>(?<!\d+)\d{1,3}(?!\d+)))?(?:[_. ](?!\d+)(?<remaining>.+)|$)`, optI)

	requestInfoRegex = re(`^(?:\[.+?\])+`, optNone)

	multiRegex = re(`[_. ](?<multi>multi)[_. ]`, optI)

	imdbRegex = re(`^(\d{1,10}|(tt)\d{1,10})$`, optNone)
)

var _ = specialEpisodeWordRegex // Sonarr keeps it, disabled; see NormalizeEpisodeTitle.

var numbers = []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}

// now is the clock for the "air date in the future" check; tests may replace it.
var now = time.Now

// ParsePath is Parser.ParsePath.
func ParsePath(path string) *ParsedEpisodeInfo { return v4d.parsePath(path) }

func (d *dialect) parsePath(path string) *ParsedEpisodeInfo {
	name := pathFileName(path)
	dirName, hasDir := pathDirectoryName(path)
	ext := pathGetExtension(name)
	// v5: ParserCommon.PathPreSubstitutionRegex on the file name (none in v4).
	for _, r := range d.pathPreSubstitution {
		r.TryReplace(&name)
	}
	result := d.parseTitle(name)

	// Parse using the folder and file separately, but combine if they both parse correctly.
	episodeNumberMatch := match(simpleEpisodeNumberRegex, name)

	if episodeNumberMatch != nil && hasDir && (result == nil || result.IsMiniSeries || len(result.AbsoluteEpisodeNumbers) > 0) {
		seasonMatch := match(d.seasonFolder, dirName)

		if seasonMatch != nil && success(seasonMatch, "season") {
			if parsed, ok := d.parsePathTitle(seasonMatch, episodeNumberMatch); ok {
				return parsed
			}
			return nil
		}
	}

	if result == nil {
		if number, ok := tryParseInt(pathFileNameWithoutExtension(name)); ok {
			result = d.parseTitle(dirName)

			switch {
			case result != nil && containsInt(result.AbsoluteEpisodeNumbers, number):
				result.AbsoluteEpisodeNumbers = []int{number}
			case result != nil && containsInt(result.EpisodeNumbers, number):
				result.EpisodeNumbers = []int{number}
			default:
				result = nil
			}
		}
	}

	if result == nil {
		result = d.parseTitle(dirName + " " + name)
	}

	if result == nil {
		result = d.parseTitle(dirName + ext)
	}

	return result
}

// parsePathTitle builds "S{season}E{first:00}[-E{last:00}][ remaining]" and parses it. ParseNumber can throw in
// Sonarr, which ParsePath does not catch; here that is reported as no result.
func (d *dialect) parsePathTitle(seasonMatch, episodeNumberMatch *regexp2.Match) (res *ParsedEpisodeInfo, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			res, ok = nil, false
		}
	}()
	episodeCaptures := captures(episodeNumberMatch, "episode")
	first := parseNumber(episodeCaptures[0].String())
	last := parseNumber(episodeCaptures[len(episodeCaptures)-1].String())
	pathTitle := fmt.Sprintf("S%sE%02d", value(seasonMatch, "season"), first)

	if first != last {
		pathTitle += fmt.Sprintf("-E%02d", last)
	}

	if success(episodeNumberMatch, "remaining") {
		pathTitle += " " + value(episodeNumberMatch, "remaining")
	}

	return d.parseTitle(pathTitle), true
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// reverseTitle undoes a reversed release name, keeping the file extension in place.
func reverseTitle(title string) string {
	without := []rune(RemoveFileExtension(title))
	all := []rune(title)
	return reverseRunes(string(without)) + string(all[len(without):])
}

// invalidDateError is Sonarr's InvalidDateException.
type invalidDateError string

func (e invalidDateError) Error() string { return string(e) }

// ParseTitle is Parser.ParseTitle. It returns nil where Sonarr returns null, including when Sonarr catches an
// exception thrown while parsing.
func ParseTitle(title string) *ParsedEpisodeInfo { return v4d.parseTitle(title) }

func (d *dialect) parseTitle(title string) (result *ParsedEpisodeInfo) {
	defer func() {
		if r := recover(); r != nil {
			result = nil
		}
	}()

	if !validateBeforeParsing(title) {
		return nil
	}

	if isMatch(d.reversedTitle, title) {
		title = reverseTitle(title)
	}

	releaseTitle := RemoveFileExtension(title)

	releaseTitle = strings.ReplaceAll(releaseTitle, "【", "[")
	releaseTitle = strings.ReplaceAll(releaseTitle, "】", "]")

	for _, r := range d.preSubstitution {
		r.TryReplace(&releaseTitle)
	}

	simpleTitle := d.simpleTitle.Replace(releaseTitle)

	// TODO: Quick fix stripping [url] - prefixes and postfixes.
	simpleTitle = websitePrefixRegex.Replace(simpleTitle)
	simpleTitle = websitePostfixRegex.Replace(simpleTitle)

	simpleTitle = cleanTorrentSuffixRegex.Replace(simpleTitle)

	simpleTitle = replaceFunc(cleanQualityBracketsRegex, simpleTitle, func(m regexp2.Match) string {
		if d.parseQualityName(m.String()).Quality != QualityUnknown {
			return ""
		}
		return m.String()
	})

	if m := match(sixDigitAirDateRegex, simpleTitle); m != nil {
		airYear := value(m, "airyear")
		airMonth := value(m, "airmonth")
		airDay := value(m, "airday")

		if airMonth != "00" || airDay != "00" {
			fixedDate := fmt.Sprintf("20%s.%s.%s", airYear, airMonth, airDay)
			simpleTitle = strings.ReplaceAll(simpleTitle, value(m, "airdate"), fixedDate)
		}
	}

	for _, regex := range d.reportTitle {
		ms := matches(regex, simpleTitle)
		if len(ms) == 0 {
			continue
		}

		res, err := d.parseMatchCollection(ms, releaseTitle)
		if err != nil {
			// InvalidDateException: stop trying patterns.
			break
		}
		if res == nil {
			continue
		}

		if res.FullSeason && containsIgnoreCase(res.ReleaseTokens, "Special") {
			res.FullSeason = false
			res.Special = true
		}

		res.Languages = d.parseLanguages(res.ReleaseTokens)

		res.Quality = d.parseQuality(title)

		res.ReleaseGroup = d.parseReleaseGroup(releaseTitle)

		if subGroup := getSubGroup(ms); strings.TrimSpace(subGroup) != "" {
			res.ReleaseGroup = &subGroup
		}

		res.ReleaseHash = getReleaseHash(ms)

		if d.v4plus {
			res.Parser = V4Plus
		}
		return res
	}

	return nil
}

// ParseSeriesName is Parser.ParseSeriesName.
func ParseSeriesName(title string) string { return v4d.parseSeriesName(title) }

func (d *dialect) parseSeriesName(title string) string {
	parseResult := d.parseTitle(title)

	if parseResult == nil {
		return d.cleanSeriesTitle(title)
	}

	return parseResult.SeriesTitle
}

// CleanSeriesTitle is Parser.CleanSeriesTitle.
func CleanSeriesTitle(title string) string { return v4d.cleanSeriesTitle(title) }

func (d *dialect) cleanSeriesTitle(title string) string {
	if strings.TrimSpace(title) == "" {
		return title
	}

	// If Title only contains numbers return it as is.
	if _, ok := parseIntDotNet(title, 64); ok {
		return title
	}

	// Replace `%` with `percent` to deal with the 3% case
	title = replace(percentRegex, title, "percent")

	if d.v5 {
		return removeDiacritics(strings.ToLower(normalizeRegex.Replace(title)))
	}
	return removeAccent(strings.ToLower(normalizeRegex.Replace(title)))
}

// NormalizeEpisodeTitle is Parser.NormalizeEpisodeTitle.
func NormalizeEpisodeTitle(title string) string {
	for _, r := range specialEpisodeTitleRegex {
		if m := match(r, title); m != nil {
			title = value(m, "episodetitle")
			break
		}
	}

	// Disabled, Until we run into specific testcases for the removal of these words.
	// title = SpecialEpisodeWordRegex.Replace(title, string.Empty);

	title = replace(partRegex, title, "")
	title = replace(punctuationRegex, title, " ")
	title = replace(duplicateSpacesRegex, title, " ")

	return strings.ToLower(trimDotNet(title))
}

// NormalizeTitle is Parser.NormalizeTitle.
func NormalizeTitle(title string) string {
	title = replace(punctuationRegex, title, "")
	title = replace(articleWordRegex, title, "")
	title = replace(duplicateSpacesRegex, title, " ")

	return strings.ToLower(trimDotNet(title))
}

// NormalizeImdbID is Parser.NormalizeImdbId. It returns "" where Sonarr returns null.
func NormalizeImdbID(imdbID string) string {
	if !isMatch(imdbRegex, imdbID) {
		return ""
	}

	if len(imdbID) > 2 {
		imdbID = strings.ReplaceAll(imdbID, "tt", "")
		for len(imdbID) < 7 {
			imdbID = "0" + imdbID
		}
		return "tt" + imdbID
	}

	return ""
}

// ParseReleaseGroup is Parser.ParseReleaseGroup. It returns nil where Sonarr returns null.
func ParseReleaseGroup(title string) *string { return v4d.parseReleaseGroup(title) }

func (d *dialect) parseReleaseGroup(title string) *string {
	title = trimDotNet(title)
	title = RemoveFileExtension(title)
	for _, r := range d.preSubstitution {
		if r.TryReplace(&title) {
			break
		}
	}

	title = websitePrefixRegex.Replace(title)
	title = cleanTorrentSuffixRegex.Replace(title)

	if m := match(animeReleaseGroupRegex, title); m != nil {
		v := value(m, "subgroup")
		return &v
	}

	title = d.cleanReleaseGroup.Replace(title)

	if ms := matches(d.exceptionReleaseGroup, title); len(ms) != 0 {
		v := value(lastMatch(ms), "releasegroup")
		return &v
	}

	if ms := matches(d.exceptionReleaseGroupExact, title); len(ms) != 0 {
		v := value(lastMatch(ms), "releasegroup")
		return &v
	}

	if ms := matches(d.releaseGroup, title); len(ms) != 0 {
		group := value(lastMatch(ms), "releasegroup")

		if _, ok := tryParseInt(group); ok {
			return nil
		}

		if isMatch(invalidReleaseGroupRegex, group) {
			return nil
		}

		return &group
	}

	return nil
}

// RemoveFileExtension is Parser.RemoveFileExtension.
func RemoveFileExtension(title string) string {
	return replaceFunc(fileExtensionRegex, title, func(m regexp2.Match) string {
		extension := strings.ToLower(m.String())
		if isMediaFileExtension(extension) || extension == ".par2" || extension == ".nzb" {
			return ""
		}
		return m.String()
	})
}

// HasMultipleLanguages is Parser.HasMultipleLanguages.
func HasMultipleLanguages(title string) bool { return isMatch(multiRegex, title) }

func (d *dialect) getSeriesTitleInfo(title string, ms []*regexp2.Match) *SeriesTitleInfo {
	info := &SeriesTitleInfo{Title: title}

	if m := match(yearInTitleRegex, title); m == nil {
		info.TitleWithoutYear = title
	} else {
		info.TitleWithoutYear = value(m, "title")
		info.Year = convertToInt32(value(m, "year"))
	}

	if mc := match(d.titleComponents, info.TitleWithoutYear); mc != nil {
		for _, c := range captures(mc, "title") {
			info.AllTitles = append(info.AllTitles, c.String())
		}
	} else if cs := captures(ms[0], "title"); len(cs) > 1 {
		for _, c := range cs {
			info.AllTitles = append(info.AllTitles, strings.NewReplacer(".", " ", "_", " ").Replace(c.String()))
		}
	}

	return info
}

func (d *dialect) parseMatchCollection(ms []*regexp2.Match, releaseTitle string) (*ParsedEpisodeInfo, error) {
	m0 := ms[0]
	seriesName := strings.NewReplacer(".", " ", "_", " ").Replace(value(m0, "title"))
	seriesName = strings.Trim(replace(requestInfoRegex, seriesName, ""), " ")

	airYear, _ := tryParseInt(value(m0, "airyear"))

	lastSeasonEpisodeStringIndex := groupEnd(m0, "title")

	var result *ParsedEpisodeInfo

	if airYear < 1900 {
		result = NewParsedEpisodeInfo()
		result.ReleaseTitle = releaseTitle

		for _, mg := range ms {
			episodeCaptures := captures(mg, "episode")
			absoluteEpisodeCaptures := captures(mg, "absoluteepisode")

			// Allows use to return a list of 0 episodes (We can handle that as a full season release)
			if len(episodeCaptures) > 0 {
				first := parseNumber(episodeCaptures[0].String())
				lastCapture := episodeCaptures[len(episodeCaptures)-1]
				last := parseNumber(lastCapture.String())

				if first > last {
					return nil, nil
				}

				result.EpisodeNumbers = intRange(first, last-first+1)

				lastSeasonEpisodeStringIndex = max(lastSeasonEpisodeStringIndex, lastCapture.Index+lastCapture.Length)

				if success(mg, "special") {
					result.Special = true
				}

				if success(mg, "splitepisode") {
					result.IsSplitEpisode = true
				}
			}

			if len(absoluteEpisodeCaptures) > 0 {
				firstCapture := absoluteEpisodeCaptures[0]
				lastCapture := absoluteEpisodeCaptures[len(absoluteEpisodeCaptures)-1]
				first := parseDecimal(firstCapture.String())
				last := parseDecimal(lastCapture.String())

				if first.Float() > last.Float() {
					return nil, nil
				}

				if !first.IsInteger() || !last.IsInteger() {
					if len(absoluteEpisodeCaptures) != 1 {
						return nil, nil // Multiple matches not allowed for specials
					}

					result.SpecialAbsoluteEpisodeNumbers = []Decimal{first}
					result.Special = true

					lastSeasonEpisodeStringIndex = max(lastSeasonEpisodeStringIndex, firstCapture.Index+firstCapture.Length)
				} else {
					f, l := int(first.Float()), int(last.Float())
					result.AbsoluteEpisodeNumbers = intRange(f, l-f+1)

					if success(mg, "special") {
						result.Special = true
					}

					lastSeasonEpisodeStringIndex = max(lastSeasonEpisodeStringIndex, lastCapture.Index+lastCapture.Length)
				}
			}

			if len(episodeCaptures) == 0 && len(absoluteEpisodeCaptures) == 0 {
				// Check to see if this is an "Extras" or "SUBPACK" release, if it is, set
				// IsSeasonExtra so they can be filtered out
				if strings.TrimSpace(value(m0, "extras")) != "" {
					result.IsSeasonExtra = true
				}

				// Partial season packs will have a seasonpart group so they can be differentiated
				// from a full season/single episode release
				seasonPart := value(m0, "seasonpart")

				if strings.TrimSpace(seasonPart) != "" {
					result.SeasonPart = convertToInt32(seasonPart)
					result.IsPartialSeason = true
				} else if d.v5 && success(m0, "special") {
					result.Special = true
				} else {
					result.FullSeason = true
				}
			}

			if len(episodeCaptures) == 2 && success(m0, "episodecount") && episodeCaptures[1].String() == value(m0, "episodecount") {
				result.EpisodeNumbers = []int{}
				result.FullSeason = true
			}
		}

		var seasons []int

		for _, c := range captures(m0, "season") {
			if parsedSeason, ok := tryParseInt(c.String()); ok {
				seasons = append(seasons, parsedSeason)
				lastSeasonEpisodeStringIndex = max(lastSeasonEpisodeStringIndex, c.Index+c.Length)
			}
		}

		if d.v5 {
			d.setSeasons5(result, seasons)
		} else {
			setSeasons4(result, seasons)
		}
	} else {
		// Try to Parse as a daily show
		var err error
		if result, err = d.parseDaily(m0, releaseTitle, airYear, &lastSeasonEpisodeStringIndex); err != nil {
			return nil, err
		}
	}

	rt := []rune(releaseTitle)
	if lastSeasonEpisodeStringIndex < len(rt) {
		result.ReleaseTokens = string(rt[lastSeasonEpisodeStringIndex:])
	} else {
		result.ReleaseTokens = releaseTitle
	}

	result.SeriesTitle = seriesName
	result.SeriesTitleInfo = d.getSeriesTitleInfo(result.SeriesTitle, ms)

	return result, nil
}

// setSeasons4 is v4's season handling at the end of ParseMatchCollection.
func setSeasons4(result *ParsedEpisodeInfo, seasons []int) {
	// If more than 1 season was parsed set IsMultiSeason to true so it can be rejected later
	for _, s := range seasons {
		if s != seasons[0] {
			result.IsMultiSeason = true
			break
		}
	}

	if len(seasons) > 0 {
		// If at least one season was parsed use the first season as the season
		result.SeasonNumber = seasons[0]
	} else if len(result.AbsoluteEpisodeNumbers) == 0 && len(result.EpisodeNumbers) > 0 {
		// If no season was found and it's not an absolute only release it should be treated as a mini series and season 1
		result.SeasonNumber = 1
		result.IsMiniSeries = true
	}
}

// parseDaily is the daily-show branch of ParseMatchCollection: the air date (and part) of the match.
func (d *dialect) parseDaily(m0 *regexp2.Match, releaseTitle string, airYear int, lastIndex *int) (*ParsedEpisodeInfo, error) {
	var airmonth, airday int

	switch {
	case success(m0, "ambiguousairmonth") && success(m0, "ambiguousairday"):
		ambiguousAirMonth := convertToInt32(value(m0, "ambiguousairmonth"))
		ambiguousAirDay := convertToInt32(value(m0, "ambiguousairday"))

		if ambiguousAirDay <= 12 && ambiguousAirMonth <= 12 {
			return nil, invalidDateError(fmt.Sprintf("Ambiguous Date, cannot validate month and day with %d and %d", ambiguousAirMonth, ambiguousAirDay))
		}

		airmonth = ambiguousAirMonth
		airday = ambiguousAirDay
	case d.v5 && success(m0, "shortairmonth"):
		shortMonthValue := value(m0, "shortairmonth")
		shortMonth, ok := shortMonths[strings.ToLower(shortMonthValue)]
		if !ok {
			return nil, invalidDateError(fmt.Sprintf("Unable to determine air month from month: %s", shortMonthValue))
		}
		airmonth = shortMonth
		airday = convertToInt32(value(m0, "airday"))
	default:
		airmonth = convertToInt32(value(m0, "airmonth"))
		airday = convertToInt32(value(m0, "airday"))

		// Swap day and month if month is bigger than 12 (scene fail)
		if !d.v5 && airmonth > 12 {
			airday, airmonth = airmonth, airday
		}
	}

	// v5 swaps after every branch (an ambiguous date too).
	if d.v5 && airmonth > 12 {
		airday, airmonth = airmonth, airday
	}

	airDate, ok := newDate(airYear, airmonth, airday)
	if !ok {
		return nil, invalidDateError(fmt.Sprintf("Invalid date found: %d-%d-%d", airYear, airmonth, airday))
	}

	// Check if episode is in the future (most likely a parse error). v5 allows early releases: up to 14 days ahead.
	ahead := 1
	if d.v5 {
		ahead = 14
	}
	n := now()
	limit := time.Date(n.Year(), n.Month(), n.Day()+ahead, 0, 0, 0, 0, time.UTC)
	if airDate.After(limit) {
		return nil, invalidDateError(fmt.Sprintf("Invalid date found: %s", airDate))
	}

	// If the parsed air date is before 1970 and the title year wasn't matched (not a match for the Plex DVR format) throw an error
	if airDate.Before(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)) && strings.TrimSpace(value(m0, "titleyear")) == "" {
		return nil, invalidDateError(fmt.Sprintf("Invalid date found: %s", airDate))
	}

	*lastIndex = max(*lastIndex, groupEnd(m0, "airyear"))
	*lastIndex = max(*lastIndex, groupEnd(m0, "airmonth"))
	*lastIndex = max(*lastIndex, groupEnd(m0, "airday"))

	result := NewParsedEpisodeInfo()
	result.ReleaseTitle = releaseTitle
	ad := airDate.Format("2006-01-02")
	result.AirDate = &ad
	if d.v5 {
		result.Parser = V5
		result.setSeasonNumbers([]int{0})
	}

	if success(m0, "part") {
		part := convertToInt32(value(m0, "part"))
		result.DailyPart = &part
	}
	return result, nil
}

// newDate is new DateTime(year, month, day), which throws on an invalid date.
func newDate(year, month, day int) (time.Time, bool) {
	if year < 1 || year > 9999 || month < 1 || month > 12 || day < 1 {
		return time.Time{}, false
	}
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if t.Day() != day {
		return time.Time{}, false
	}
	return t, true
}

func intRange(start, count int) []int {
	out := make([]int, count)
	for i := range out {
		out[i] = start + i
	}
	return out
}

func validateBeforeParsing(title string) bool {
	lower := strings.ToLower(title)
	if strings.Contains(lower, "password") && strings.Contains(lower, "yenc") {
		return false
	}

	hasLetterOrDigit := false
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			hasLetterOrDigit = true
			break
		}
	}
	if !hasLetterOrDigit {
		return false
	}

	titleWithoutExtension := RemoveFileExtension(title)

	for _, r := range rejectHashedReleasesRegexes {
		if isMatch(r, titleWithoutExtension) {
			return false
		}
	}

	for _, r := range seasonFolderRegexes {
		if isMatch(r, titleWithoutExtension) {
			return false
		}
	}

	return true
}

func getSubGroup(ms []*regexp2.Match) string {
	if success(ms[0], "subgroup") {
		return value(ms[0], "subgroup")
	}
	return ""
}

func getReleaseHash(ms []*regexp2.Match) string {
	if success(ms[0], "hash") {
		hashValue := strings.Trim(value(ms[0], "hash"), "[]()")

		if hashValue == "1280x720" {
			return ""
		}

		return hashValue
	}
	return ""
}

func parseNumber(s string) int {
	normalized := convertToNumerals(norm.NFKC.String(s))

	if n, ok := tryParseInt(normalized); ok {
		return n
	}

	lower := strings.ToLower(s)
	for i, w := range numbers {
		if w == lower {
			return i
		}
	}

	panic(formatError(s + " isn't a number"))
}

func parseDecimal(s string) Decimal {
	normalized := convertToNumerals(norm.NFKC.String(s))

	if d, ok := parseDecimalText(normalized); ok {
		return d
	}

	panic(formatError(s + " isn't a number"))
}

// convertToNumerals is Parser.ConvertToNumerals for decimal digits (Unicode Nd). Other numeric characters (Nl, No)
// keep their text; after the NFKC step only rare ones remain (see the package doc).
func convertToNumerals(input string) string {
	var b strings.Builder
	for _, r := range input {
		if v, ok := digitValue(r); ok {
			b.WriteByte(byte('0' + v))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// digitValue is char.GetNumericValue for a Unicode decimal digit. Every Nd run starts at a zero and runs in tens.
func digitValue(r rune) (int, bool) {
	if !unicode.IsDigit(r) {
		return 0, false
	}
	if r >= '0' && r <= '9' {
		return int(r - '0'), true
	}
	start := r
	for unicode.IsDigit(start - 1) {
		start--
	}
	return int(r-start) % 10, true
}
