# Security Disclosure Process

If you believe that you have discovered a security or privacy vulnerability in this fork, report it using the [GitHub private vulnerability feature](https://github.com/stephenlclarke/container-builder-shim/security/advisories/new). Include the affected product and version, a technical description of the observed and expected behavior, reproduction steps, and a proof of concept or exploit.

Report fork-specific vulnerabilities here, including issues introduced by the `stephenlclarke` builder image, packaging, release metadata, build configuration, context handling, or fork-only changes. If the same issue reproduces unchanged against Apple upstream, report it to the relevant Apple upstream repository and note that upstream reference in this fork's advisory. For third-party dependency vulnerabilities, include the dependency name, version, advisory or CVE reference, and whether the vulnerable path is reachable in this fork.

The project team will do their best to acknowledge receiving all security reports within 7 days of submission. This initial acknowledgment is neither acceptance nor rejection of your report. The project team may come back to you with further questions or invite you to collaborate while working through the details of your report.

Keep these additional guidelines in mind when submitting your report:

- Reports concerning known, publicly disclosed CVEs can be submitted as normal issues to this project.
- Output from automated security scans or fuzzers MUST include additional context demonstrating the vulnerability with a proof of concept or working exploit.
- Application crashes due to malformed inputs are typically not treated as security vulnerabilities, unless they are shown to also impact other processes on the system.

Do not include credentials, registry tokens, certificates, SSH keys, API keys, personal data, private registry names, or proprietary container images in reports, tests, examples, logs, or screenshots.

This project is independent of Apple and is not eligible for Apple Security Bounties.
