import { expect, it } from "vitest";
import { withTMDbSources } from "./tmdb-key";

it("adds the missing TMDb sources with their public addresses", () => {
  const config: Record<string, unknown> = {};
  withTMDbSources(config);
  expect(config).toEqual({ sources: { tmdb: { url: "https://api.themoviedb.org/3" }, "tmdb-images": { url: "https://image.tmdb.org/t/p" } } });
});

it("keeps an address, a budget, an env name and other sources that are already set", () => {
  const config: Record<string, unknown> = {
    series: { enabled: true },
    sources: {
      tmdb: { url: "http://tmdb-proxy.example/3", api_key_env: "THEATARR_TMDB_KEY", budget: "tmdb" },
      "tmdb-images": { url: "", budget: "images" },
      tvdb: { url: "https://api4.thetvdb.com/v4", api_key_env: "THEATARR_TVDB_KEY" },
    },
  };
  withTMDbSources(config);
  expect(config).toEqual({
    series: { enabled: true },
    sources: {
      tmdb: { url: "http://tmdb-proxy.example/3", api_key_env: "THEATARR_TMDB_KEY", budget: "tmdb" },
      "tmdb-images": { url: "https://image.tmdb.org/t/p", budget: "images" },
      tvdb: { url: "https://api4.thetvdb.com/v4", api_key_env: "THEATARR_TVDB_KEY" },
    },
  });
});
