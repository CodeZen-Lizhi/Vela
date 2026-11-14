import test from "node:test";
import { generateKeyPairSync, createHash, sign } from "node:crypto";
import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync, spawn } from "node:child_process";
import { createServer } from "node:net";
import { setTimeout } from "node:timers/promises";
import { root, syncVersion, frontendArguments } from "./release-version.mjs";
import { sourceState, checksums, sha256, verifyUpdate } from "./release.mjs";

/** 为每项测试创建隔离文件目录，结束后只清理该目录。 */
function temporary(t) {
  const directory = mkdtempSync(join(tmpdir(), "vela-release-test-"));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  return directory;
}

test("版本从根 package.json 派生，检查模式能拒绝漂移", (t) => {
  const directory = temporary(t);
  mkdirSync(join(directory, "frontend"));
  mkdirSync(join(directory, "internal/buildinfo"), { recursive: true });
  writeFileSync(join(directory, "package.json"), JSON.stringify({ version: "0.1.0-beta.1", license: "MIT" }));
  writeFileSync(join(directory, "frontend/package.json"), JSON.stringify({ version: "old" }));
  writeFileSync(join(directory, "package-lock.json"), JSON.stringify({ version: "old", packages: { "": {}, frontend: {} } }));
  writeFileSync(join(directory, "internal/buildinfo/buildinfo.go"), "old");
  assert.throws(() => syncVersion(true, directory), /不同步/);
  assert.equal(syncVersion(false, directory), "0.1.0-beta.1");
  assert.equal(syncVersion(true, directory), "0.1.0-beta.1");
  assert.match(readFileSync(join(directory, "internal/buildinfo/buildinfo.go"), "utf8"), /const Version = "0.1.0-beta.1"/);
  assert.equal(JSON.parse(readFileSync(join(directory, "frontend/package.json"))).license, "MIT");
});

test("源码指纹区分已暂存、未暂存与未跟踪文件内容", (t) => {
  const directory = temporary(t);
  const git = (...args) => execFileSync("git", args, { cwd: directory, stdio: "pipe" });
  git("init", "-q");
  writeFileSync(join(directory, "source.txt"), "original");
  git("add", "source.txt");
  git("-c", "user.name=Vela Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgSign=false", "commit", "-qm", "fixture");
  const clean = sourceState(directory);
  assert.equal(clean.dirty, false);
  writeFileSync(join(directory, "source.txt"), "edited");
  const unstaged = sourceState(directory);
  assert.equal(unstaged.dirty, true);
  assert.notEqual(unstaged.trackedDiffSHA256, clean.trackedDiffSHA256);
  git("add", "source.txt");
  const staged = sourceState(directory);
  assert.notDeepEqual(staged.status, unstaged.status);
  writeFileSync(join(directory, "new.txt"), "first");
  const first = sourceState(directory);
  writeFileSync(join(directory, "new.txt"), "second");
  const second = sourceState(directory);
  assert.equal(first.untracked[0].path, "new.txt");
  assert.notEqual(first.untracked[0].sha256, second.untracked[0].sha256);
});

test("SHA256SUMS 包含真实文件内容，排除目录与自身", (t) => {
  const directory = temporary(t);
  writeFileSync(join(directory, "vela.dmg"), "candidate");
  writeFileSync(join(directory, "SHA256SUMS"), "stale");
  mkdirSync(join(directory, "Vela.app"));
  assert.equal(checksums(directory), `${sha256("candidate")}  vela.dmg\n`);
});

test("前端 dev/build 保留 Vite 附加参数", () => {
  assert.deepEqual(frontendArguments("dev", ["--port", "5197"]), ["--port", "5197"]);
  assert.deepEqual(frontendArguments("build", ["--outDir", "/tmp/vela-dist"]), ["build", "--outDir", "/tmp/vela-dist"]);
});

test("npm workspace dev 在指定端口启动，并注入根版本", { timeout: 20000 }, async (t) => {
  const probe = createServer();
  await new Promise((resolve) => probe.listen(0, "127.0.0.1", resolve));
  const port = probe.address().port;
  await new Promise((resolve) => probe.close(resolve));
  const child = spawn("npm", ["--workspace", "vela-frontend", "run", "dev", "--", "--port", String(port), "--host", "127.0.0.1"], { cwd: root, detached: true, stdio: ["ignore", "pipe", "pipe"] });
  let output = "";
  child.stdout.on("data", (data) => { output += data; });
  child.stderr.on("data", (data) => { output += data; });
  t.after(() => { if (child.exitCode === null) process.kill(-child.pid, "SIGTERM"); });
  let response;
  for (let attempt = 0; attempt < 60; attempt++) {
    if (child.exitCode !== null) assert.fail(`Vite 提前退出：${output}`);
    try { response = await fetch(`http://127.0.0.1:${port}/src/App.svelte`); } catch {}
    if (response?.ok) break;
    await setTimeout(150);
  }
  assert.ok(response?.ok, output);
  const code = await response.text();
  assert.ok(code.includes(JSON.parse(readFileSync(join(root, "package.json"))).version));
  assert.match(code, /VITE_VELA_VERSION/);
});


/** 更新发布门禁验证真实签名，拒绝篡改内容及错误长度。 */
test("更新候选拒绝不匹配大小与伪造签名", (t) => {
  const directory = temporary(t);
  const { publicKey, privateKey } = generateKeyPairSync("ed25519");
  const key = publicKey.export({ type: "spki", format: "der" }).subarray(-32).toString("base64");
  const content = Buffer.from("signed update");
  const manifest = { url: "https://example.test/update.tar.gz", size: content.length, signature: sign(null, createHash("sha256").update(content).digest(), privateKey).toString("base64") };
  writeFileSync(join(directory, "update.tar.gz"), content);
  writeFileSync(join(directory, "update.json"), JSON.stringify(manifest));
  assert.equal(verifyUpdate(directory, "update.json", key), "update.tar.gz");
  writeFileSync(join(directory, "update.tar.gz"), Buffer.from("tamper update"));
  assert.throws(() => verifyUpdate(directory, "update.json", key), /签名/);
  writeFileSync(join(directory, "update.tar.gz"), "short");
  assert.throws(() => verifyUpdate(directory, "update.json", key), /大小/);
});
