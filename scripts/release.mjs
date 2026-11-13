import { copyFileSync, existsSync, lstatSync, mkdirSync, readFileSync, readlinkSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { parseArgs } from "node:util";
import { binaryPath } from "mygo-cli";
import { root, syncVersion } from "./release-version.mjs";
import { notices } from "./release-notices.mjs";

/** 对文件或源码状态计算 SHA256，写入候选包的可追溯信息。 */
export function sha256(value) {
  return createHash("sha256").update(value).digest("hex");
}

/** 记录当前提交、标签及未提交内容指纹；只记录摘要，不导出源码或凭据。 */
export function sourceState(directory = root) {
  const git = (...args) => execFileSync("git", args, { cwd: directory, maxBuffer: 64 * 1024 * 1024 });
  const status = git("status", "--porcelain=v1", "-z", "--untracked-files=all").toString();
  const untracked = git("ls-files", "--others", "--exclude-standard", "-z").toString().split("\0").filter(Boolean).sort().map((path) => {
    const file = join(directory, path);
    return { path, sha256: sha256(lstatSync(file).isSymbolicLink() ? readlinkSync(file) : readFileSync(file)) };
  });
  return {
    commit: git("rev-parse", "HEAD").toString().trim(),
    tags: git("tag", "--points-at", "HEAD").toString().trim().split("\n").filter(Boolean).sort(),
    dirty: status.length > 0,
    status: status.split("\0").filter(Boolean),
    trackedDiffSHA256: sha256(git("diff", "--binary", "HEAD", "--")),
    untracked,
  };
}

/** 枚举可交付文件，按相对路径稳定排序；App 目录通过 ZIP 记录整体摘要。 */
export function checksums(directory) {
  return readdirSync(directory, { withFileTypes: true }).filter((entry) => entry.isFile() && entry.name !== "SHA256SUMS").map((entry) => `${sha256(readFileSync(join(directory, entry.name)))}  ${entry.name}`).sort().join("\n") + "\n";
}

/** 构建一个本地 macOS 候选版本，拒绝覆盖已有候选包和隐式外部发布。 */
function prepareRelease() {
  const { values } = parseArgs({ options: { notes: { type: "string" }, platform: { type: "string", default: `darwin/${process.arch === "x64" ? "amd64" : process.arch}` }, "allow-dirty": { type: "boolean", default: false }, help: { type: "boolean", default: false } } });
  if (values.help) {
    console.log("npm run release:prepare -- --notes <release-notes.md> [--platform darwin/arm64|darwin/amd64] [--allow-dirty]");
    return;
  }
  if (process.platform !== "darwin") throw new Error("App 签名和 DMG 打包必须在 macOS 上执行");
  if (!/^darwin\/(arm64|amd64)$/.test(values.platform)) throw new Error("本地候选包仅支持 darwin/arm64 或 darwin/amd64");
  if (!values.notes) throw new Error("请通过 --notes 提供主 agent 确认的发布说明");
  const notes = readFileSync(resolve(root, values.notes));
  if (!notes.toString().trim()) throw new Error("发布说明不能为空");
  const version = syncVersion(true);
  const expectedNotices = notices();
  if (readFileSync(join(root, "THIRD_PARTY_NOTICES.txt"), "utf8") !== expectedNotices) throw new Error("第三方许可清单不同步；请运行 npm run licenses:generate");
  const source = sourceState();
  if (source.dirty && !values["allow-dirty"]) throw new Error("工作树有未提交改动；本地验收可显式传入 --allow-dirty，源码状态将记入候选包");
  // 额外链接参数或验收标签会改变正式包内容；候选命令保持当前 MyGo 的生产默认值。
  for (const key of ["GOFLAGS", "MYGO_LDFLAGS", "MYGO_INSPECTOR", "MYGO_AUTOMATION", "MYGO_CLI_BINARY"]) {
    if (process.env[key]) throw new Error(`请清除 ${key} 后再准备候选包`);
  }
  const platform = values.platform.replace("/", "-");
  const candidate = join(root, "build/release", version, platform);
  if (existsSync(candidate)) throw new Error(`候选目录已存在，不覆盖：${candidate}`);
  mkdirSync(join(root, "build/release", version), { recursive: true });
  // MyGo 自身会原子替换平台目录；输入文件放在独立目录，不污染 App 的资源来源。
  const inputs = join(root, "build/release", version, `.inputs-${platform}`);
  mkdirSync(inputs);
  const metadataFile = join(inputs, "release-metadata.json");
  const metadata = {
    version, platform: values.platform, createdAt: new Date().toISOString(), source,
    sourceStateSHA256: sha256(JSON.stringify(source)),
    expectedTag: `v${version}`, notesSHA256: sha256(notes),
    packageLockSHA256: sha256(readFileSync(join(root, "package-lock.json"))),
    licenseSHA256: sha256(readFileSync(join(root, "LICENSE"))),
    thirdPartyNoticesSHA256: sha256(expectedNotices),
    tools: { node: process.version, go: execFileSync("go", ["version"], { encoding: "utf8" }).trim(), mygo: JSON.parse(readFileSync(join(root, "node_modules/mygo-cli/package.json"))).version },
  };
  writeFileSync(metadataFile, JSON.stringify(metadata, null, 2) + "\n");
  execFileSync(binaryPath(), ["build", "-platform", values.platform, "-o", relative(root, join(root, "build/release", version))], {
    cwd: root, stdio: "inherit", env: { ...process.env, VELA_RELEASE_METADATA: metadataFile },
  });
  if (JSON.stringify(sourceState()) !== JSON.stringify(source)) throw new Error("构建期间源码发生变化；此候选包不可用于发布，请重新准备");
  const app = join(candidate, "Vela.app");
  const resources = join(app, "Contents/Resources");
  const arch = values.platform.split("/")[1];
  const dmg = `vela-${version}-${arch}.dmg`;
  if (!existsSync(join(candidate, dmg))) throw new Error(`MyGo 没有生成预期 DMG：${dmg}`);
  for (const file of ["LICENSE", "THIRD_PARTY_NOTICES.txt"]) {
    if (!readFileSync(join(resources, file)).equals(readFileSync(join(root, file)))) throw new Error(`App 中的许可文件不匹配：${file}`);
    copyFileSync(join(root, file), join(candidate, file));
  }
  if (!readFileSync(join(resources, "release-metadata.json")).equals(readFileSync(metadataFile))) throw new Error("App 缺少匹配的源码元数据");
  const bundleVersion = execFileSync("/usr/libexec/PlistBuddy", ["-c", "Print :CFBundleShortVersionString", join(app, "Contents/Info.plist")], { encoding: "utf8" }).trim();
  if (bundleVersion !== version) throw new Error(`App 版本不一致：${bundleVersion}`);
  execFileSync("codesign", ["--verify", "--deep", "--strict", app], { stdio: "inherit" });
  const goBuildInfo = execFileSync("go", ["version", "-m", join(app, "Contents/MacOS/Vela")], { encoding: "utf8" });
  copyFileSync(metadataFile, join(candidate, "release-metadata.json"));
  writeFileSync(join(candidate, "RELEASE_NOTES.md"), notes);
  const archive = `vela-${version}-${arch}.app.zip`;
  execFileSync("ditto", ["-c", "-k", "--sequesterRsrc", "--keepParent", app, join(candidate, archive)], { stdio: "inherit" });
  writeFileSync(join(candidate, "release-manifest.json"), JSON.stringify({ ...metadata, app: "Vela.app", archive, dmg, bundleVersion, goBuildInfo, signing: "MyGo config: ad hoc by default; no notarization configured" }, null, 2) + "\n");
  writeFileSync(join(candidate, "SHA256SUMS"), checksums(candidate));
  rmSync(inputs, { recursive: true });
  console.log(`本地候选包已准备：${candidate}\n源码：${source.commit}${source.dirty ? " (dirty)" : ""}\nSHA256SUMS 已生成；未执行外部发布。`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) prepareRelease();
