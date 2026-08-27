#!/usr/bin/env node
const { spawn } = require('node:child_process');
const { existsSync } = require('node:fs');
const { dirname, join } = require('node:path');

const PKG = {
  'darwin-arm64': '@becrafter/sail-darwin-arm64',
  'darwin-x64':   '@becrafter/sail-darwin-x64',
  'linux-arm64':  '@becrafter/sail-linux-arm64',
  'linux-x64':    '@becrafter/sail-linux-x64',
};

const key = `${process.platform}-${process.arch}`;
const pkg = PKG[key];
if (!pkg) {
  console.error(`sail: unsupported platform ${key}`);
  process.exit(1);
}

let pkgDir;
try {
  pkgDir = dirname(require.resolve(`${pkg}/package.json`));
} catch {
  console.error(`sail: platform package ${pkg} not installed. Try: npm install ${pkg}`);
  process.exit(1);
}

const binName = 'sail';
const binPath = join(pkgDir, 'bin', binName);
if (!existsSync(binPath)) {
  console.error(`sail: binary not found at ${binPath}`);
  process.exit(1);
}

const child = spawn(binPath, process.argv.slice(2), { stdio: 'inherit' });
child.on('error', e => { console.error(`sail: ${e.message}`); process.exit(1); });
child.on('close', code => process.exit(code ?? 0));
