# Release Signatures

This directory stores release digest files and detached signatures.

## Layout

```
signatures/
  pubkeys/
    <fingerprint>.asc
  trusted-keys.txt
  <version>/
    SHA256SUMS
    SHA256SUMS.asc
```

## Flow

1. Build binaries locally with deterministic flags.
2. Generate `SHA256SUMS`.
3. Sign `SHA256SUMS` with a trusted GPG key.
4. Commit files under `signatures/<version>/`.
5. Tag and push.
6. GitHub Actions rebuilds binaries and checks that checksums match exactly.
7. Workflow verifies `SHA256SUMS.asc` and uploads binaries + digest + signature to the release.

## Trusted Keys

Trusted signer fingerprints are listed in `trusted-keys.txt` and public keys are kept in `pubkeys/`.
