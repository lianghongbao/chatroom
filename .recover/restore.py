#!/usr/bin/env python3
"""从 Claude Code 的会话记录里恢复丢失的服务实现文件。

数据来源两个：
  1. ~/.claude/file-history/<session>/<hash>@vN   —— 编辑前快照
  2. ~/.claude/projects/*/*.jsonl 里的 toolUseResult —— Read 工具读回的全文

两者都只覆盖「被读过或被编辑过」的文件，所以恢复是部分的。
脚本把能恢复的写回 app/<域>/service/ 下的新布局。
"""

import json
import glob
import os
import sys

ROOT = os.path.expanduser("~/.claude/projects/-mnt-d-Learn-go-project")
HIST = os.path.expanduser("~/.claude/file-history")
DEST = "/mnt/d/Learn/go-project/chatroom/_rec_tmp"

SERVICES = ("user-service", "friend-service", "conversation-service",
            "message-service", "ws-gateway-service")


def load_file_history():
    """hash 文件名 → 路径，来自 transcript 里的 file-history-delta / snapshot。"""
    mapping = {}
    for s in glob.glob(f"{ROOT}/*.jsonl"):
        for line in open(s, encoding="utf-8"):
            if "file-history" not in line:
                continue
            try:
                d = json.loads(line)
            except Exception:
                continue
            t = d.get("type")
            if t == "file-history-delta":
                tp = d.get("trackingPath", "")
                bf = (d.get("backup") or {}).get("backupFileName")
                if bf:
                    mapping[(os.path.basename(s)[:-6], bf)] = tp
            elif t == "file-history-snapshot":
                for p, b in (d.get("snapshot", {}).get("trackedFileBackups") or {}).items():
                    bf = b.get("backupFileName") if isinstance(b, dict) else b
                    if bf:
                        mapping[(os.path.basename(s)[:-6], bf)] = p
    return mapping


def collect():
    """返回 {相对路径: (来源, 内容)}，只保留 <域>-service/ 下的文件。"""
    out = {}

    # 1) file-history 快照
    for (sid, bf), path in load_file_history().items():
        if not any(s in path for s in SERVICES):
            continue
        fp = os.path.join(HIST, sid, bf)
        if not os.path.exists(fp):
            continue
        try:
            ver = int(bf.split("@v")[-1])
        except ValueError:
            ver = 0
        rel = path.split("chatroom/", 1)[-1]
        # 取版本号最大的
        prev = out.get(rel)
        if prev is None or (prev[0] == "fh" and ver > prev[2]):
            out[rel] = ("fh", open(fp, encoding="utf-8").read(), ver)

    # 2) transcript 里的完整 Read（无 offset/limit）
    calls = {}
    for s in glob.glob(f"{ROOT}/*.jsonl"):
        for line in open(s, encoding="utf-8"):
            if '"tool_use"' not in line:
                continue
            try:
                d = json.loads(line)
            except Exception:
                continue
            for c in ((d.get("message") or {}).get("content") or []):
                if isinstance(c, dict) and c.get("type") == "tool_use" and c.get("name") == "Read":
                    fp = (c.get("input") or {}).get("file_path", "")
                    if "/chatroom/" in fp:
                        calls[c["id"]] = ((c.get("input") or {}).get("offset"),
                                           (c.get("input") or {}).get("limit"))

    for s in glob.glob(f"{ROOT}/*.jsonl"):
        for line in open(s, encoding="utf-8"):
            if "toolUseResult" not in line:
                continue
            try:
                d = json.loads(line)
            except Exception:
                continue
            tur = d.get("toolUseResult")
            if not isinstance(tur, dict):
                continue
            f = tur.get("file")
            if not isinstance(f, dict):
                continue
            fp = f.get("filePath") or ""
            content = f.get("content")
            if "/chatroom/" not in fp or not isinstance(content, str) or not content:
                continue
            off, lim = calls.get(tur.get("toolUseId") or "", (None, None))
            if off or lim:
                continue  # 部分读，跳过
            rel = fp.split("/chatroom/", 1)[-1]
            prev = out.get(rel)
            if prev is None or (prev[0] == "tr" and len(content) > len(prev[1])):
                out[rel] = ("tr", content, 0)
    return out


def target_path(rel):
    """旧路径 → 新布局路径。返回 None 表示不该恢复（已由 api//pkg/ 覆盖）。"""
    svc, _, rest = rel.partition("/")
    if svc not in SERVICES:
        return None
    # api/ 下的 proto 已经移到 contracts → api/，不恢复
    if rest.startswith("api/"):
        return None
    # pkg/ 下的旧副本已经上移到根 pkg/
    if rest.startswith("pkg/"):
        return None
    # internal/{middleware,testutil,errors,observability}/ 已经上移到 pkg/
    for moved in ("internal/middleware/", "internal/testutil/", "internal/errors/",
                  "internal/observability/", "internal/utils/keys.go"):
        if rest.startswith(moved):
            return None
    # ent 生成代码可以重新生成
    if "/data/ent/" in rest:
        return None
    # <域>-service/ → <域>/service/（新布局）
    domain = svc[:-len("-service")] if svc.endswith("-service") else svc
    return os.path.join(DEST, "app", domain, "service", rest)


def main():
    data = collect()
    written, skipped = [], []
    for rel, (src, content, _v) in sorted(data.items()):
        tp = target_path(rel)
        if tp is None:
            skipped.append(rel)
            continue
        os.makedirs(os.path.dirname(tp), exist_ok=True)
        with open(tp, "w", encoding="utf-8") as fh:
            fh.write(content)
        written.append((rel, tp, len(content)))

    print(f"恢复 {len(written)} 个文件：")
    for rel, tp, n in written:
        print(f"  {rel}  ({n}B)")
    print(f"\n跳过 {len(skipped)} 个（已在 api//pkg/ 或可重新生成）")


if __name__ == "__main__":
    main()
