import { spawnSync } from 'node:child_process';
import { chmodSync, mkdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { binaryPath, targets } from './platforms.mjs';
import { validateVersion } from './release.mjs';

const root = fileURLToPath(new URL('../', import.meta.url));
const { version } = JSON.parse(readFileSync(path.join(root, 'package.json'), 'utf8'));
validateVersion(version);
for (const target of targets) {
  const output = path.join(root, binaryPath(target.platform, target.arch));
  mkdirSync(path.dirname(output), { recursive: true });
  console.log(`Building nugs ${version} for ${target.platform}/${target.arch}`);
  const result = spawnSync('go', [
    'build', '-mod=readonly', '-trimpath', '-buildvcs=false',
    '-ldflags', `-s -w -X workspace-nuggets.Version=${version}`,
    '-o', output, './cmd/nugs',
  ], {
    cwd: root,
    env: { ...process.env, CGO_ENABLED: '0', GOOS: target.goos, GOARCH: target.goarch, GOAMD64: 'v1', GOARM64: 'v8.0', GOWORK: 'off', GOFLAGS: '' },
    stdio: 'inherit',
  });
  if (result.error) throw result.error;
  if (result.status !== 0) process.exit(result.status ?? 1);
  chmodSync(output, 0o755);
}
