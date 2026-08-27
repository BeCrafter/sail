#!/usr/bin/env node
// 在发布暂存区(<stage>/<platform>/)内设置版本号、查重并 npm publish。
// 只读写暂存区副本,不碰源码树 npm/*/package.json(模板保持 0.0.0)。
// 暂存区(含二进制)由 scripts/release.sh 复制模板 + 编译产出,位于 dist/。
//
// 用法: node scripts/publish-npm.js <version> [--dry-run] [--stage <dir>]
//   --stage  暂存区目录(默认 npm,仅手动测试用;正式发布由 release.sh 传 dist)
// 发布目标 registry 由 NPM_REGISTRY 环境变量控制(默认官方 https://registry.npmjs.org/),
// 由 release.sh 导入;所有 npm view/publish 均显式带 --registry,避免本地镜像误发。
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const ROOT = path.resolve(__dirname, '..');
const REGISTRY = process.env.NPM_REGISTRY || 'https://registry.npmjs.org/';

const PLATFORMS = [
  { key: 'darwin-arm64', name: '@becrafter/sail-darwin-arm64' },
  { key: 'darwin-x64',   name: '@becrafter/sail-darwin-x64' },
  { key: 'linux-arm64',  name: '@becrafter/sail-linux-arm64' },
  { key: 'linux-x64',    name: '@becrafter/sail-linux-x64' },
];
const MAIN = { name: '@becrafter/sail', dir: 'main' };

const ver = (process.argv[2] || '').replace(/^v/, '');
const dryRun = process.argv.includes('--dry-run');

function getArg(name) {
  const i = process.argv.indexOf(name);
  if (i !== -1 && process.argv[i + 1]) return process.argv[i + 1];
  const eq = process.argv.find(a => a.startsWith(name + '='));
  return eq ? eq.slice(name.length + 1) : undefined;
}

const STAGE_ARG = getArg('--stage') || 'npm';
const stageDir = path.isAbsolute(STAGE_ARG) ? STAGE_ARG : path.join(ROOT, STAGE_ARG);

if (!ver || !/^\d+\.\d+\.\d+$/.test(ver)) {
  console.error('usage: node scripts/publish-npm.js <version> [--dry-run] [--stage <dir>]');
  process.exit(1);
}
if (!fs.existsSync(stageDir)) {
  console.error(`stage dir not found: ${stageDir}`);
  process.exit(1);
}

const pkgFile = rel => path.join(stageDir, rel, 'package.json');

function setVersions() {
  for (const p of PLATFORMS) {
    const f = pkgFile(p.key);
    const pkg = JSON.parse(fs.readFileSync(f, 'utf8'));
    pkg.version = ver;
    fs.writeFileSync(f, JSON.stringify(pkg, null, 2) + '\n');
  }
  const f = pkgFile(MAIN.dir);
  const pkg = JSON.parse(fs.readFileSync(f, 'utf8'));
  pkg.version = ver;
  if (pkg.optionalDependencies) {
    for (const k of Object.keys(pkg.optionalDependencies)) pkg.optionalDependencies[k] = ver;
  }
  fs.writeFileSync(f, JSON.stringify(pkg, null, 2) + '\n');
}

function isPublished(name, version) {
  const r = spawnSync('npm', ['view', `${name}@${version}`, 'version', '--registry', REGISTRY], { encoding: 'utf8' });
  return r.stdout.trim() === version;
}

function publish(rel, label) {
  const args = ['publish', '--access', 'public', '--registry', REGISTRY];
  if (dryRun) args.push('--dry-run');
  console.log(`${dryRun ? 'dry-run ' : ''}publish ${label} → ${REGISTRY}`);
  const r = spawnSync('npm', args, { cwd: path.join(stageDir, rel), stdio: 'inherit' });
  if (r.status !== 0) {
    console.error(`publish 失败: ${label}`);
    process.exit(1);
  }
}

setVersions();

let published = 0, skipped = 0;
for (const p of PLATFORMS) {
  if (!dryRun && isPublished(p.name, ver)) {
    console.log(`skip  ${p.name}@${ver} (已发布)`);
    skipped++;
  } else {
    publish(p.key, `${p.name}@${ver}`);
    published++;
  }
}
if (!dryRun && isPublished(MAIN.name, ver)) {
  console.log(`skip  ${MAIN.name}@${ver} (已发布)`);
  skipped++;
} else {
  publish(MAIN.dir, `${MAIN.name}@${ver}`);
  published++;
}
console.log(`\n完成: ${published} 发布, ${skipped} 跳过 (暂存区: ${stageDir})`);
