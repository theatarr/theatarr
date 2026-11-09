package demo

// The invented catalogue. Every name here was made up for the demo: none is a real film, show, studio, network or
// release group. Years, genres and season shapes are fixed; overviews, dates, sizes and the rest come from the seed.

// movieSpec is one invented movie. scenario says what state its versions are in (see scenarios in library.go).
type movieSpec struct {
	name     string
	year     int // 0: this year or next (an upcoming title)
	genres   []string
	scenario string
}

// seriesSpec is one invented series: the episode count of each season, in order.
type seriesSpec struct {
	name     string
	year     int
	genres   []string
	network  string
	seasons  []int
	scenario string
}

var movieSpecs = []movieSpec{
	{"The Saffron Observatory", 2017, []string{"Drama"}, scCeilingKnown},
	{"The Waxwing Almanac", 2018, []string{"Mystery"}, scCeilingLikely},
	{"Juniper Signal", 2019, []string{"Science Fiction"}, scCeilingHigher},
	{"The Quiet Astrolabe", 2020, []string{"Drama"}, scCeilingUnknown},
	{"The Brassmoor Accord", 2019, []string{"Drama", "Thriller"}, scComplete},
	{"Quillon Drift", 2021, []string{"Science Fiction", "Adventure"}, scComplete},
	{"Saltglass Meridian", 2018, []string{"Mystery"}, scUpgrade},
	{"Ninefold Lantern", 2022, []string{"Fantasy", "Adventure"}, scPartial},
	{"A Ledger of Small Hours", 2017, []string{"Drama"}, scComplete},
	{"Copperline Express", 2020, []string{"Action", "Thriller"}, scComplete},
	{"The Vellichor Tapes", 2023, []string{"Horror", "Mystery"}, scMissing},
	{"Orrin Halvard Takes the Long Way", 2016, []string{"Comedy", "Drama"}, scComplete},
	{"Glasswing Protocol", 2024, []string{"Thriller", "Science Fiction"}, scPartial},
	{"Moth Season at Kerrow", 2015, []string{"Drama", "Romance"}, scComplete},
	{"Tidewater Requiem", 2019, []string{"War", "Drama"}, scUpgrade},
	{"The Marrowfield Heist", 2022, []string{"Crime", "Comedy"}, scComplete},
	{"Seven Lamps for Ysolde", 2021, []string{"Romance", "Fantasy"}, scComplete},
	{"Driftlock", 0, []string{"Science Fiction", "Action"}, scUpcoming},
	{"Halcyon Spindle", 2018, []string{"Animation", "Family"}, scComplete},
	{"The Unbound Cartographer", 2020, []string{"Adventure", "Drama"}, scPartial},
	{"Pellucid", 2023, []string{"Thriller"}, scComplete},
	{"Wren and the Iron Orchard", 2017, []string{"Family", "Fantasy"}, scUnmonitored},
	{"Cinderhook", 2024, []string{"Western", "Drama"}, scMissing},
	{"Static Over Vashmere", 2016, []string{"Horror"}, scComplete},
	{"The Tallowmere Inheritance", 2021, []string{"Mystery", "Drama"}, scUpgrade},
	{"Lowlight Choir", 2019, []string{"Music", "Drama"}, scComplete},
	{"Fennick's Last Engine", 2022, []string{"Science Fiction", "Drama"}, scPartial},
	{"A Quiet Hum of Bees", 2018, []string{"Drama"}, scComplete},
	{"Gravelgold Run", 2020, []string{"Western", "Adventure"}, scComplete},
	{"The Oskarvane Experiment", 0, []string{"Science Fiction", "Horror"}, scUpcoming},
	{"Paper Kites over Dunmarra", 2017, []string{"Drama", "History"}, scComplete},
	{"Hollowmark", 2023, []string{"Horror", "Thriller"}, scPartial},
	{"Ember Ledger", 2021, []string{"Thriller", "Crime"}, scComplete},
	{"The Clockwright of Vennick Row", 2016, []string{"Fantasy", "Family"}, scUpgrade},
	{"Undertow Signal", 2024, []string{"Thriller", "Mystery"}, scMissing},
	{"Brightwater Fugue", 2019, []string{"Drama", "Music"}, scComplete},
	{"Marigold Static", 2022, []string{"Comedy", "Romance"}, scComplete},
	{"Kestrel Nine", 2020, []string{"Action", "Science Fiction"}, scPartial},
	{"The Plumtree Ledger", 2018, []string{"Drama"}, scUnmonitored},
	{"Velvet Arithmetic", 2021, []string{"Comedy", "Romance"}, scComplete},
	{"Northlight Cannery", 2017, []string{"Drama"}, scComplete},
	{"Sablewind", 0, []string{"Adventure", "Fantasy"}, scUpcoming},
	{"The Gentle Saboteurs", 2019, []string{"Comedy", "Crime"}, scUpgrade},
	{"Ironwood Lullaby", 2022, []string{"War", "Drama"}, scComplete},
	{"Corvid Hour", 2024, []string{"Mystery", "Thriller"}, scPartial},
	{"Larkspur Junction", 2016, []string{"Western"}, scComplete},
	{"Teaspoon Rebellion", 2020, []string{"Comedy", "Family"}, scComplete},
	{"Ashfall over Quenby", 0, []string{"Action", "Drama"}, scUpcoming},
	{"The Myrtlewick Papers", 2018, []string{"Mystery", "Crime"}, scUpgrade},
	{"Gossamer Engine", 2021, []string{"Science Fiction"}, scComplete},
	{"Hinterwold Waltz", 2019, []string{"Romance", "Drama"}, scUnmonitored},
	{"Bramblecourt", 2023, []string{"Horror"}, scMissing},
	{"The Lanternfish Society", 2022, []string{"Adventure", "Comedy"}, scComplete},
	{"Cobaltine", 2017, []string{"Thriller", "Action"}, scComplete},
	{"Featherweight Tyrant", 2020, []string{"Comedy"}, scUpgrade},
	{"The Drowned Orrery", 0, []string{"Adventure", "Mystery"}, scUpcoming},
	{"Murmuration Theory", 2018, []string{"Drama", "Science Fiction"}, scComplete},
	{"Quartz and Quarrel", 2021, []string{"Comedy", "Crime"}, scMissing},
	{"Ravelstone", 0, []string{"Fantasy", "Adventure"}, scUpcoming},
}

// animeMovieSpecs are anime films (version anime-movie). The two "Aokaze Requiem" films belong to the franchise.
var animeMovieSpecs = []movieSpec{
	{"Kumoyuki no Hanabira", 2019, []string{"Animation", "Drama", "Romance"}, scComplete},
	{"Tsukimori Station", 2021, []string{"Animation", "Fantasy"}, scComplete},
	{"Aokaze Requiem: The Movie", 2022, []string{"Animation", "Action", "Fantasy"}, scComplete},
	{"Hakuren Garden", 2018, []string{"Animation", "Family"}, scMissing},
	{"Aokaze Requiem: Ember of Dawn", 0, []string{"Animation", "Action", "Fantasy"}, scUpcoming},
}

var seriesSpecs = []seriesSpec{
	{"The Harrowgate Files", 2019, []string{"Crime", "Drama"}, "Northfield One", []int{8, 8, 8}, scComplete},
	{"Lantern Street Station", 2020, []string{"Drama"}, "Halden Broadcasting", []int{10, 10}, scPartial},
	{"Copper Sky Cartel", 2021, []string{"Crime", "Thriller"}, "Streamwell", []int{8, 8}, scComplete},
	{"Wickmere", 2018, []string{"Mystery", "Drama"}, "Northfield One", []int{6, 6, 6, 6}, scComplete},
	{"Saltmarsh Rangers", 2022, []string{"Adventure", "Family"}, "Brightcast", []int{10, 10}, scAiring},
	{"The Pellbrook Inquiries", 2017, []string{"Mystery", "Crime"}, "Halden Broadcasting", []int{6, 6, 6}, scUpgrade},
	{"Orbitfall", 2023, []string{"Science Fiction", "Drama"}, "Streamwell", []int{8, 8}, scAiring},
	{"Kindling Hall", 2020, []string{"Drama", "History"}, "Brightcast", []int{8, 8, 8}, scPartial},
	{"Tinker and Thorne", 2021, []string{"Comedy"}, "Pinemoor TV", []int{10, 10}, scComplete},
	{"Glasshouse Republic", 0, []string{"Drama", "Politics"}, "Streamwell", []int{8}, scAiring},
	{"Mossgrave", 2016, []string{"Horror", "Mystery"}, "Pinemoor TV", []int{10, 10, 10}, scComplete},
	{"The Quiet Ledger", 2022, []string{"Thriller"}, "Northfield One", []int{6, 6}, scMissing},
	{"Ferrous Bay", 2019, []string{"Drama"}, "Halden Broadcasting", []int{10, 10, 10}, scPartial},
	{"Nightjar Protocol", 0, []string{"Science Fiction", "Thriller"}, "Brightcast", []int{8}, scAiring},
	{"Brindlewood Bakers", 2023, []string{"Comedy", "Family"}, "Pinemoor TV", []int{12, 12}, scComplete},
}

var animeSeriesSpecs = []seriesSpec{
	{"Kohaku Observatory", 2016, []string{"Animation", "Drama"}, "Studio Harukaze", []int{12}, scCeilingKnown},
	{"Aozora Almanac", 2017, []string{"Animation", "Mystery"}, "Studio Harukaze", []int{12}, scCeilingLikely},
	{"Tsukikage Signal", 2018, []string{"Animation", "Science Fiction"}, "Studio Harukaze", []int{12}, scCeilingHigher},
	{"Komorebi Astrolabe", 2019, []string{"Animation", "Drama"}, "Studio Harukaze", []int{12}, scCeilingUnknown},
	{"Aokaze Requiem", 2016, []string{"Animation", "Action", "Fantasy"}, "Studio Harukaze", []int{12, 12}, scComplete},
	{"Kiraboshi Mechanics", 2019, []string{"Animation", "Science Fiction"}, "Kobaru Works", []int{24}, scComplete},
	{"Yumegatari Express", 2021, []string{"Animation", "Fantasy"}, "Studio Harukaze", []int{12}, scPartial},
	{"Hanabi Cartographers", 2022, []string{"Animation", "Adventure"}, "Tsubame Pictures", []int{13}, scUpgrade},
	{"Tsuratsura Detective Office", 2018, []string{"Animation", "Mystery", "Comedy"}, "Kobaru Works", []int{12, 12}, scComplete},
	{"Shirokane Blade Academy", 2020, []string{"Animation", "Action"}, "Tsubame Pictures", []int{24}, scPartial},
	{"Mizuiro Lighthouse", 2023, []string{"Animation", "Drama"}, "Studio Harukaze", []int{12}, scMissing},
	{"Kagerou Postal Service", 0, []string{"Animation", "Fantasy", "Drama"}, "Kobaru Works", []int{12}, scAiring},
	{"Ginrei Frontier", 2017, []string{"Animation", "Science Fiction", "Action"}, "Tsubame Pictures", []int{26}, scComplete},
	{"Nanairo Bento Club", 0, []string{"Animation", "Comedy"}, "Studio Harukaze", []int{12}, scAiring},
}

// Words the generator draws names, overviews and release groups from. All invented or ordinary words.
var (
	firstNames = []string{"Orrin", "Ysolde", "Maren", "Tobiah", "Calla", "Idris", "Wren", "Sefton", "Liesl", "Peregrine",
		"Junia", "Hollis", "Brannoc", "Elsabet", "Corwin", "Tamsin", "Aurek", "Delphine", "Ottoline", "Rafe"}
	lastNames = []string{"Halvard", "Quenby", "Marrow", "Thistlewood", "Vane", "Ashcombe", "Pellick", "Dunmore", "Carrow",
		"Ellery", "Fenwright", "Larkin", "Morrow", "Oakhurst", "Strand", "Tolliver"}
	roles = []string{"a retired lighthouse keeper", "a cartographer with a forged licence", "a night-shift archivist",
		"a disgraced courier", "a travelling clock repairer", "a young ferry pilot", "a reluctant heir",
		"a canal engineer", "a radio operator", "a fairground mechanic", "a junior coroner", "a beekeeper"}
	places = []string{"the salt flats of Kerrow", "a drowned market town", "the copper hills above Vashmere",
		"a city of shuttered observatories", "an island that appears once a decade", "the last station on the line",
		"a valley of abandoned mills", "a floating border town", "the frozen harbour at Dunmarra", "an orbital orchard"}
	goals = []string{"recover a ledger that should not exist", "deliver one final letter", "map a coast that keeps moving",
		"keep a promise made twenty years ago", "find who silenced the town's bells", "win back a stolen engine",
		"outrun a debt with interest measured in years", "solve a murder nobody reported",
		"rebuild a choir after a flood", "decode a broadcast from nowhere"}
	twists = []string{"Every answer costs a memory.", "Nobody is who the census says.", "The weather has opinions.",
		"Help arrives from the least likely neighbour.", "The map, it turns out, is drawing them.",
		"Old rivalries surface with the tide.", "Time runs differently after midnight.",
		"The truth is buried under the bandstand."}
	taglines = []string{"Some doors only open once.", "Keep the lights on.", "Every map has a margin.",
		"Nothing stays buried.", "The tide always returns.", "Listen to the static.", "One more stop.",
		"Count the hours."}
	episodeAdj  = []string{"Quiet", "Copper", "Hollow", "Salt", "Last", "Glass", "Paper", "Iron", "Amber", "Low", "Bright", "Silent"}
	episodeNoun = []string{"Harbour", "Ledger", "Lantern", "Signal", "Orchard", "Engine", "Choir", "Crossing", "Archive",
		"Bell", "Ferry", "Meridian", "Tide", "Window"}
	// Release groups: invented tags.
	movieGroups  = []string{"QUILLON", "BRASSMR", "LANTRN", "OSPRYX", "VELLUM", "TIDEFX", "KESTRL", "CINDRA"}
	seriesGroups = []string{"HARROW", "WICKR", "MOSSGV", "FERRUS"}
	animeGroups  = []string{"Mossbell-Subs", "Inkreed-Raws", "Glimmerfen-Encodes", "Copperwick-Fansub"}
	webSources   = []string{"AMZN", "NF", "ATVP", "DSNP", "MAX"}
)
