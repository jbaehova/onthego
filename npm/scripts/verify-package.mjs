import { access, readdir } from "node:fs/promises";

const expected = [
  "npm/bin/onthego.js",
  "npm/vendor/onthego-darwin-arm64",
  "npm/vendor/onthego-linux-amd64",
];

for (const file of expected) {
  await access(file);
}

const rootEntries = await readdir(".");
const forbidden = [".env.example", "Dockerfile", ".dockerignore"];
for (const file of forbidden) {
  if (rootEntries.includes(file)) {
    throw new Error(`${file} must not be shipped`);
  }
}
