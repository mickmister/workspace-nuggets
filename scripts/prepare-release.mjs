import { appendFileSync, existsSync, readFileSync, writeFileSync } from 'node:fs';
import { releaseFromTag } from './release.mjs';

const release = releaseFromTag(process.argv[2] ?? process.env.GITHUB_REF_NAME);
const filename = new URL('../package.json', import.meta.url);
const pkg = JSON.parse(readFileSync(filename, 'utf8'));
pkg.version = release.version;
writeFileSync(filename, `${JSON.stringify(pkg, null, 2)}\n`);
const lockfile = new URL('../package-lock.json', import.meta.url);
if (existsSync(lockfile)) {
  const lock = JSON.parse(readFileSync(lockfile, 'utf8'));
  lock.version = release.version;
  lock.packages[''].version = release.version;
  writeFileSync(lockfile, `${JSON.stringify(lock, null, 2)}\n`);
}
if (process.env.GITHUB_OUTPUT) {
  appendFileSync(process.env.GITHUB_OUTPUT, `version=${release.version}\ndist_tag=${release.distTag}\n`);
}
console.log(JSON.stringify(release));
