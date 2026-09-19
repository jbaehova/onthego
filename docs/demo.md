# ONTHEGO live demo

The demo uses the five Phase 1 commands. Run it from a Git project that has at least one supported agent session.

```sh
npm install -g @jbaehova/onthego
onthego init
onthego login
onthego pass
onthego status
onthego pull
```

`onthego pass` has no agent or session option. It scans local Codex, Cursor, Hermes, and Claude storage. It keeps every session associated with the current Git project, redacts secrets locally, creates one handoff envelope, and sends it to Daytona.

Expected demo beats:

1. Run `onthego init`. Show the stable project ID and trackable `.onthego.yaml` marker.
2. Show `onthego --help`. Only `init`, `login`, `pass`, `status`, and `pull` appear.
3. Run `onthego pass`. Explain that switching coding agents does not require changing the command.
4. Run `onthego status`. Point out the synchronized Daytona state, redacted log excerpt, and GPT-5.6 Luna summary.
5. Run `onthego pull`. Point out that the artifact is retained only after receipt and SHA-256 verification.

GPU fine-tuning, model serving, and Hugging Face connectors are backlog work. The live Phase 1 demo uses the Daytona CPU `agent-sync` workflow.
