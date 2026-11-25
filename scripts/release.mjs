// Strict SemVer: do not silently normalize a tag into another npm version.
const core = '(0|[1-9][0-9]*)';
const pre = '(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)';
const semver = new RegExp(`^${core}\\.${core}\\.${core}(?:-(${pre}(?:\\.${pre})*))?(?:\\+[0-9A-Za-z-]+(?:\\.[0-9A-Za-z-]+)*)?$`);

export function validateVersion(version) {
  if (typeof version !== 'string' || version.trim() !== version || !semver.test(version)) {
    throw new Error(`Invalid npm release version: ${version}`);
  }
  return version;
}

export function releaseFromTag(tag) {
  if (typeof tag !== 'string' || !tag.startsWith('v')) {
    throw new Error('Release tags must start with v, for example v1.2.3');
  }
  const version = validateVersion(tag.slice(1));
  const prerelease = semver.exec(version)[4];
  // Match springboard: prereleases never move the production latest dist-tag.
  const distTag = prerelease ? (prerelease.startsWith('rc') ? 'rc' : 'dev') : 'latest';
  return { version, distTag };
}
