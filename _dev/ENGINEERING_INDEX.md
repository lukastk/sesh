# Engineering memory — on-demand index

Current operating rules: [AGENTS.md](../AGENTS.md). Current short watchlist:
[AGENTS.local.md](../AGENTS.local.md). Read only the history relevant to the task,
including its corrections and follow-ups; dates and “DEPLOYED” statements are
historical evidence, **not current truth**. Do not eagerly import these archives.

## Archives and provenance

- [H1–H90](AGENTS.local.archive.md): existing archive, June–August 2026,
  archived in two parts on 2026-08-22 and 2026-09-17. Its old references to a live
  H91+ log describe the layout at that time, not the current layout.
- [H91–H121](engineering-history-h91-h121.md): original tracked root
  AGENTS.local.md preserved **byte-for-byte after the archive marker**, including
  its preface, all dates, follow-ups, duplicated H104 numbering, trap digest and
  foundational reference. Entries cover August–October 2026. The historical
  preface's relative paths retain repository-root meaning.

Find an entry without loading an entire archive (commands from repository root):

```sh
rg -n '^##+ H(118|119|120|121)' _dev/engineering-history-h91-h121.md
rg -n 'your topic' _dev/engineering-history-h91-h121.md _dev/AGENTS.local.archive.md
```

Then read the matching section and any correction, not just the matching line.
When adding substantial findings, use a dated `_dev/` topic/history doc and add a
link here; keep the eager memory small. Check existing H numbers before assigning
new ones. Preserve tracking/privacy and original evidence when archiving.

## Topic routes into H91–H121

| Work area | Entries to inspect |
|---|---|
| Pi RPC delivery / commands / completion | H120 experiments → H121 implementation, tests, deploy, open transcript-CWD issue; [PI_DELIVERY](PI_DELIVERY.md) |
| Claude held sessions / unexpected backgrounding | H119 **actual-cause and correction sections first**: empty-prompt left-arrow gesture, not proof the user typed `/background`; no-agent-view pin, force-revive semantics |
| Codex writer locks / resume / transcripts | H118 detached app-server pin; H102 title-helper session stamps; H93 reply extraction and duplicate formats |
| Verified caller identity / provenance | H92 incident; H95 flag-specific remedies; H112 gate; H113 turn ancestry; H114 harness source and correction to “not a thread”; [IDENTITY](IDENTITY.md) |
| Trust / agent defaults / scheduling | H110 Claude trust; H100 configured default; H111 scheduler + typing guard and follow-up model-test/details fixes |
| Cockpit navigation / which-client law | H117 session-qualified targets; H116 latency and rejected mirror; H106 sidebar race; H98 including both follow-ups |
| Sidebar view ring / phone quick key | H115 and both follow-ups; H116 follow-up pinned explicit view |
| TUI scope / hold / glyphs / scrolling | H104 **two separate entries** (hold glyphs and CWD scope); H105 archive inheritance; H103 release; H107 clipboard; H97 head glyphs; H94 shell viewport; H91 goto |
| Mesh cost / phone resources | H99 scale pass; H102 fork/WAL improvements; H109 status options; H108 **follow-up 3 corrects the headline**; [MESH_SCALE](MESH_SCALE.md) |
| Cross-repo myrig/boxyard operations | H101 probe deadlines; H96 derived boxes and upstream-fix follow-up (historical, not permission to edit another repo) |
| Test failures and safety | H91 vacuous cursor test; H92/H93/H95 filtered-run traps; H113 accidental conformance/leaks; H118 live-kill incident; H121 fixture/overlay techniques; [TESTING](TESTING.md) |
| Deploy / skill copies / daemon environment | H112 stale installed copies; H115 binary-before-config; H121 last dated fleet report; [OPERATIONS](OPERATIONS.md) |

## Older shorthand still used in designs

Search [H1–H90](AGENTS.local.archive.md) by H number. Particularly: H22 Termux
CGO/DNS; H25 claim declaration; H30 pipeline exit status; H33 full render target
list; H41 duplicated geometry/dispatch drift; H44 exact neuter restoration;
H49/H63 dirty-checkout deploy hazards; H70 running sidebar binaries; H74/H75
environment/isolation/leaked processes; H82 inherited background-job identity;
H89 tmux marker inheritance; H90 tmux command-size limits. The new archive's final
“Trap digest” and “Reference” sections retain the earlier summaries in full.
