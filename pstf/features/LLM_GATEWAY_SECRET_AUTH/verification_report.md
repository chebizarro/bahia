# LLM gateway secret authentication verification

Bahia accepts an absolute `auth_token_file` for each HTTP LLM gateway endpoint.
The application reads and trims the file at startup and passes the resolved
token only to the in-memory HTTP route-manager configuration.

Verification:

- Focused configuration, application, and LLM adapter tests pass.
- Tests cover successful file resolution, missing and empty files, relative
  paths, and conflicting inline plus file-backed sources.
- The LLM routes user guide documents the production file-backed form without
  a credential value.
