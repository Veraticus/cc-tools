import { writeFileSync } from "node:fs";
import process from "node:process";

const marker = process.env.STEWARD_PACKAGE_SMOKE_ENTRY_MARKER;
if (marker === undefined) throw new Error("package smoke entry marker is required");
writeFileSync(marker, JSON.stringify({ entry: process.argv[1], node: process.execPath }));
