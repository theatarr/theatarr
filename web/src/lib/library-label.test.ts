import { expect, it } from "vitest";
import { libraryLabel } from "./library-label";

it.each([
 ["movie-web", "web-2160p", "4K WEB"],
 ["movie-remux", "remux-uhd", "4K Remux"],
 ["tv-2160p", "web-2160p", "4K WEB"],
 ["anime-tv", "web-1080p", "Anime"],
 ["custom", "custom", "custom"],
])("names %s using its profile", (key, profile, label) => {
 expect(libraryLabel(key, profile)).toBe(label);
});
