package anime

import "fmt"

// The requests the pipeline makes. The cache adapter keys on them and the live adapters send them.

// AniListQuery is the media query, whitespace-collapsed as the offline cache key has it.
const AniListQuery = "query($ids:[Int]){Page(perPage:50){media(id_in:$ids,type:ANIME){ id idMal type format status " +
	"episodes duration isAdult countryOfOrigin title { romaji english native } synonyms startDate { year month day } " +
	"endDate { year month day } season seasonYear relations { edges { relationType(version: 2) node { id type format " +
	"title { romaji } } } } }}}"

// KitsuURL is the Kitsu request for one anime with its media relationships.
func KitsuURL(id int) string {
	return fmt.Sprintf("https://kitsu.app/api/edge/anime/%d?include=mediaRelationships.destination,mappings"+
		"&fields[anime]=canonicalTitle,titles,subtype,startDate,endDate,episodeCount,episodeLength,"+
		"mediaRelationships,mappings"+
		"&fields[mediaRelationships]=role,destination&fields[mappings]=externalSite,externalId", id)
}

// SkyHookURL is Sonarr's metadata proxy URL for a TVDB series.
func SkyHookURL(id int) string {
	return fmt.Sprintf("https://skyhook.sonarr.tv/v1/tvdb/shows/en/%d", id)
}

// XEMURL is TheXEM's map/all URL for a TVDB series.
func XEMURL(id int) string { return fmt.Sprintf("https://thexem.info/map/all?id=%d&origin=tvdb", id) }

// The static lists' default URLs.
const (
	URLScudLee   = "https://raw.githubusercontent.com/Anime-Lists/anime-lists/master/anime-list-master.xml"
	URLFribb     = "https://raw.githubusercontent.com/Fribb/anime-lists/master/anime-list-full.json"
	URLAnibridge = "https://github.com/anibridge/anibridge-mappings/releases/download/v3/mappings.json"
	URLManami    = "https://github.com/manami-project/anime-offline-database/releases/latest/download/anime-offline-database-minified.json"
	URLAniDB     = "https://anidb.net/api/anime-titles.xml.gz"
	URLSeaDex    = "https://releases.moe"
)
