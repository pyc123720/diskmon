// 新 issue 的自动分诊：打标签 + 首次回复。
// 只负责「收集材料」和「设定期望」，不自动关闭、不代替判断。
// 调用方：.github/workflows/triage.yml（actions/github-script）

const ZH = [
  "谢谢，已经收到。",
  "",
  "这是一个人维护的项目：**我通常一周内看一次**（工作日晚上和周末），不会当天回。不是不理，是只有这么多时间。",
  "",
  "为了让我的下一次动作有效，顺手做两件事：",
  "",
  "1. 在 exe 所在目录运行 `diskmon-go.exe diag`，把生成的 `diag-*.txt` 内容贴进来 —— 版本、系统、数据目录、上次扫描、告警、日志尾巴一次齐。",
  "2. 如果和**清理**有关：先别动隔离区。文件在 `<数据目录>\\quarantine`，7 天内都能还原，贴一下隔离项 id。",
  "",
  "常见问题（SmartScreen 报毒、清理后空间没变、计划 4 GB 实际只有几 MB、进程归因不准）：docs/FAQ.md",
  "",
  "标签含义：",
  "- `needs-info` —— 在等你的补充材料，补齐后我会重新看；",
  "- `needs-triage` —— 还没定性，可能是 bug 也可能是设计如此；",
  "- `bug` / `enhancement` —— 已确认，进入待修 / 待做。",
];

const EN = [
  "Thanks — got it.",
  "",
  "Single-maintainer project: **I usually look at issues within a week** (evenings and weekends), not same-day. It's a time limit, not a brush-off.",
  "",
  "To make my next step useful:",
  "",
  "1. Run `diskmon-go.exe diag` next to the exe and paste the generated `diag-*.txt` (version, OS, data dir, last scan, alerts, log tail — all at once).",
  "2. If it involves **cleanup**: don't touch quarantine yet. Files live in `<data-dir>\\quarantine` and stay restorable for 7 days — include the quarantine item id.",
  "",
  "Common questions (SmartScreen, \"space didn't change\", \"planned 4 GB but only a few MB\", attribution accuracy): docs/FAQ.md",
  "",
  "Labels:",
  "- `needs-info` — waiting on your details;",
  "- `needs-triage` — not classified yet;",
  "- `bug` / `enhancement` — confirmed.",
];

const THIN_ZH = "> 这条现在信息不够，我先挂了 `needs-info`。补上诊断包后我会立刻重新判断。";
const THIN_EN =
  "> This report is missing the details above, so I labelled it `needs-info`. Once the diag bundle is in, I'll re-check right away.";

module.exports = async ({ github, context, core }) => {
  const issue = context.payload.issue;
  const repo = context.repo;
  if (!issue) {
    core.info("no issue payload");
    return;
  }
  const body = issue.body || "";
  const combined = issue.title + "\n" + body;
  const isZh = /[\u4e00-\u9fa5]/.test(combined);

  // 维护者自己开的 issue 不打扰
  if (issue.user.login === repo.owner) {
    core.info("author is the maintainer, skipping auto-reply");
    return;
  }

  const ensureLabel = async (name, color, description) => {
    try {
      await github.rest.issues.getLabel({ ...repo, name });
    } catch (e) {
      try {
        await github.rest.issues.createLabel({ ...repo, name, color, description });
      } catch (err) {
        core.info(`label ${name} not creatable: ${err.message}`);
      }
    }
  };
  await ensureLabel("needs-triage", "d4c5f9", "等确认的问题");
  await ensureLabel("needs-info", "fbca04", "缺少复现材料，等补充");
  await ensureLabel("stale", "ededed", "长期没有新消息");

  // 材料够不够：Issue Form 出来的一般都带版本行和诊断段
  const looksComplete = body.length > 600 && /版本|version/i.test(body);
  const thin = !looksComplete;

  const labels = ["needs-triage"];
  if (thin) labels.push("needs-info");
  try {
    await github.rest.issues.addLabels({ ...repo, issue_number: issue.number, labels });
  } catch (e) {
    core.info(`addLabels failed: ${e.message}`);
  }

  const lines = isZh ? ZH.slice() : EN.slice();
  if (thin) lines.push("", isZh ? THIN_ZH : THIN_EN);

  try {
    await github.rest.issues.createComment({
      ...repo,
      issue_number: issue.number,
      body: lines.join("\n"),
    });
  } catch (e) {
    core.info(`comment failed: ${e.message}`);
  }
};
