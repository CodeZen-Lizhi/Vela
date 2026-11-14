#!/bin/sh
set -eu

# 先在目标目录完成复制和签名检查，失败时不退出旧版、不修改现有应用。
source_app=${1:?缺少安装源}
target_app=${2:?缺少安装目标}
stage=$(/usr/bin/mktemp -d "$(/usr/bin/dirname "$target_app")/.vela-install.XXXXXX")

# 若最终换名失败，先恢复旧版；恢复失败时保留备份目录，不能随清理删除。
cleanup() {
  if [ -e "$stage/previous.app" ] && [ ! -e "$target_app" ]; then
    if ! /bin/mv "$stage/previous.app" "$target_app"; then
      echo "旧版应用保留在 $stage/previous.app，请恢复后重试。" >&2
      return
    fi
  fi
  /bin/rm -rf "$stage"
}
trap cleanup EXIT
/usr/bin/ditto "$source_app" "$stage/Vela.app"
/usr/bin/codesign --verify --deep --strict "$stage/Vela.app"
if [ "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$stage/Vela.app/Contents/Info.plist")" != com.codezenlizhi.vela ]; then
  echo "安装源不是 Vela，已停止安装。" >&2
  exit 1
fi
executable="$target_app/Contents/MacOS/Vela"

# 只匹配即将替换的完整路径，其他位置的开发版或独立副本不受影响。
find_app_pids() {
  /bin/ps -axo pid=,comm= | /usr/bin/awk -v executable="$executable" '
    { command = $0; sub(/^[[:space:]]*[0-9]+[[:space:]]+/, "", command) }
    command == executable { print $1 }
  '
}
for pid in $(find_app_pids); do
  # MyGo 将一次 SIGTERM 转为正常退出；不能重发或升级为强杀。
  if /bin/kill -0 "$pid" 2>/dev/null; then
    /bin/kill -TERM "$pid"
  fi
done
attempt=0
while [ -n "$(find_app_pids)" ]; do
  if [ "$attempt" -ge 30 ]; then
    echo "Vela 在 30 秒内未能退出，安装已停止。请退出旧版后重试，原应用未被替换。" >&2
    exit 1
  fi
  /bin/sleep 1
  attempt=$((attempt + 1))
done
if [ -e "$target_app" ]; then
  /bin/mv "$target_app" "$stage/previous.app"
fi
/bin/mv "$stage/Vela.app" "$target_app"
