import { readFile, writeFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import { fileURLToPath } from "node:url";

const root = new URL("../../", import.meta.url);
const dist = new URL("../dist/", import.meta.url);
const js = await readFile(new URL("app.js", dist));
const css = await readFile(new URL("style.css", dist));
const version = createHash("sha256")
  .update(js)
  .update(css)
  .digest("hex")
  .slice(0, 12);
const html = (await readFile(new URL("index.html", dist), "utf8"))
  .replace("/assets/app.js", `/assets/app.js?v=${version}`)
  .replace("/assets/style.css", `/assets/style.css?v=${version}`);
if (!html.includes(`/assets/style.css?v=${version}`))
  throw new Error("Missing bundled CSS");
for (const [name, content] of [
  ["app.js", js],
  ["style.css", css],
  ["index.html", html],
]) {
  await writeFile(new URL(name, root), content);
}
console.log(`Published React assets ${version} to ${fileURLToPath(root)}`);
