import { access, readFile } from "node:fs/promises";

const required = ["src/index.html", "src/styles.css"];
await Promise.all(required.map((file) => access(file)));

const html = await readFile("src/index.html", "utf8");
for (const marker of ["<title>Scraps", "<main", "styles.css"]) {
  if (!html.includes(marker)) throw new Error(`index.html is missing ${marker}`);
}

const sections = [...html.matchAll(/<section\b/g)];
if (sections.length !== 3) throw new Error(`expected hero + two stories, found ${sections.length} sections`);
for (const marker of ["Turn your idle compute into a private agent cloud.", "01 / Install it on a spare computer", "02 / Use it from Pi", "Proxmox", "/scrap"]) {
  if (!html.includes(marker)) throw new Error(`index.html is missing ${marker}`);
}

console.log("landing checks passed");
