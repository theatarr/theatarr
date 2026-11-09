// vite build empties internal/webui/dist; put back the committed placeholder so git sees no change.
import { writeFileSync } from "node:fs";
writeFileSync(new URL("../../internal/webui/dist/.keep", import.meta.url), "");
