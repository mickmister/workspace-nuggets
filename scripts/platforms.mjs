import path from 'node:path';

export const targets = [
  { platform: 'linux', arch: 'x64', goos: 'linux', goarch: 'amd64' },
  { platform: 'linux', arch: 'arm64', goos: 'linux', goarch: 'arm64' },
  { platform: 'darwin', arch: 'x64', goos: 'darwin', goarch: 'amd64' },
  { platform: 'darwin', arch: 'arm64', goos: 'darwin', goarch: 'arm64' },
  { platform: 'win32', arch: 'x64', goos: 'windows', goarch: 'amd64' },
  { platform: 'win32', arch: 'arm64', goos: 'windows', goarch: 'arm64' },
];

export function binaryPath(platform, arch) {
  if (!targets.some(target => target.platform === platform && target.arch === arch)) {
    throw new Error(`nugs does not provide a prebuilt binary for ${platform}/${arch}. Build the included Go source with: go build -o nugs ./cmd/nugs`);
  }
  return path.join('binaries', `${platform}-${arch}`, platform === 'win32' ? 'nugs.exe' : 'nugs');
}
