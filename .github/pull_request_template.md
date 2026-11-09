# Proposed changes

Add more detail *here* about the release

## Linked issue

`Refs #123` or `Fixes #123`, or `N/A`.

## Security

What input is untrusted, what secret passes through, what can an attacker reach? Paste the
`make security` result and the `security-auditor` summary.

## Website

Does the download site still describe the app correctly after this change? Write
`Website audit: no change needed` or `Website audit: updated …` (see `.agents/rules/90-website-audit.md`).

## Review goals and guidelines

This section is a *must* to properly test do a code review.

1. Run the code locally and test the functionality before even looking at the code. If it doesn’t work then no point in reviewing.
2. Look over the new code line by line to understand what is being introduced.
3. Ask clarifying questions to make sure you understand anything that isn’t obvious.
4. Meet up to go over large reviews and have the developer explain the code.
5. Always do reviews in the morning before starting on your own work so you’re not context switching.
