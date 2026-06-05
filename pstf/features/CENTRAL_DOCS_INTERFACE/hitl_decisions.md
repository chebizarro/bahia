# CENTRAL_DOCS_INTERFACE HITL Decisions

Updated: 2026-06-05

## Decisions recorded

No new human decision was required for Item 8 verification. The implementation follows the plan decisions already made for Items 1-7:

- `docs/user-guide/**/*.md` remains the single documentation source of truth.
- `/api/v1/docs` and `/api/v1/docs/{topic}` are read-only HTTP-native docs/query routes, not signer-first mutation routes.
- Assistant prompts continue to use the existing encrypted ContextVM `assistant/prompt` operation; documentation context is attached only through visible `selected_refs` such as `docs:features-services`.
- Route-derived docs refs are visible and dismissible in the assistant composer.

## Follow-up decisions

None for this slice. The product question about richer Markdown frontmatter/descriptions remains outside Item 8 and is not required for the verified central docs interface.
