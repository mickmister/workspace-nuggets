import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { binaryPath, targets } from '../../scripts/platforms.mjs';
import { releaseFromTag } from '../../scripts/release.mjs';

test('release version is exactly the tag without v; prereleases do not publish to latest', () => {
  for (const [tag, version, distTag] of [
    ['v1.2.3', '1.2.3', 'latest'],
    ['v0.1.0-rc.1', '0.1.0-rc.1', 'rc'],
    ['v2.0.0-dev4', '2.0.0-dev4', 'dev'],
    ['v2.0.0-beta.2', '2.0.0-beta.2', 'dev'],
    ['v1.2.3+build.4', '1.2.3+build.4', 'latest'],
  ]) assert.deepEqual(releaseFromTag(tag), { version, distTag });
});

test('malformed/noncanonical tags cannot be normalized into a release', () => {
  for (const tag of [undefined, '', '1.2.3', 'vv1.2.3', 'v01.2.3', 'v1.2', 'v1.2.3-01', 'v1.2.3-', 'v1.2.3\n', 'v1.2.3;exit', 'v1.2.3-rc..1']) {
    assert.throws(() => releaseFromTag(tag), undefined, String(tag));
  }
});

test('release preparation updates package and lockfile exactly and emits workflow outputs', t => {
  const root = mkdtempSync(path.join(tmpdir(), 'nugs release '));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  mkdirSync(path.join(root, 'scripts'));
  for (const name of ['release.mjs', 'prepare-release.mjs']) {
    copyFileSync(new URL(`../../scripts/${name}`, import.meta.url), path.join(root, 'scripts', name));
  }
  writeFileSync(path.join(root, 'package.json'), JSON.stringify({ name: '@mickmister/nugs', version: '0.0.0-development' }));
  writeFileSync(path.join(root, 'package-lock.json'), JSON.stringify({ name: '@mickmister/nugs', version: '0.0.0-development', packages: { '': { name: '@mickmister/nugs', version: '0.0.0-development' } } }));
  const output = path.join(root, 'outputs');
  execFileSync(process.execPath, [path.join(root, 'scripts', 'prepare-release.mjs')], {
    env: { ...process.env, GITHUB_REF_NAME: 'v3.2.1-rc.2', GITHUB_OUTPUT: output },
  });
  assert.equal(JSON.parse(readFileSync(path.join(root, 'package.json'))).version, '3.2.1-rc.2');
  assert.equal(JSON.parse(readFileSync(path.join(root, 'package.json'))).name, '@mickmister/nugs');
  const lock = JSON.parse(readFileSync(path.join(root, 'package-lock.json')));
  assert.equal(lock.version, '3.2.1-rc.2');
  assert.equal(lock.packages[''].version, '3.2.1-rc.2');
  assert.equal(lock.name, '@mickmister/nugs');
  assert.equal(lock.packages[''].name, '@mickmister/nugs');
  assert.equal(readFileSync(output, 'utf8'), 'version=3.2.1-rc.2\ndist_tag=rc\n');
  const before = readFileSync(path.join(root, 'package.json'), 'utf8');
  assert.throws(() => execFileSync(process.execPath, [path.join(root, 'scripts', 'prepare-release.mjs'), 'v01.2.3'], { stdio: 'pipe' }));
  assert.equal(readFileSync(path.join(root, 'package.json'), 'utf8'), before);
});

test('every supported platform resolves to a bundled executable and unsupported ones fail explicitly', () => {
  assert.equal(targets.length, 6);
  for (const target of targets) {
    assert.equal(binaryPath(target.platform, target.arch), path.join('binaries', `${target.platform}-${target.arch}`, target.platform === 'win32' ? 'nugs.exe' : 'nugs'));
  }
  assert.throws(() => binaryPath('freebsd', 'x64'), /included Go source/);
  assert.throws(() => binaryPath('linux', 'ia32'), /linux\/ia32/);
});
