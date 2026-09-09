# Security reporting

Report vulnerabilities privately through the repository's GitHub Security Advisories page when private reporting is enabled. If unavailable, contact the repository owner privately through their GitHub profile to arrange a secure channel before sharing exploit details. Do not disclose credentials or sensitive repository content in public issues.

Include the VCM version, operating system, a minimal reproduction using disposable repositories, and the expected and observed behavior. Redact secrets from hooks, URLs, logs, and manifests.

Workspace configuration can execute shell hooks with the invoking user's privileges. Only run trusted configuration. Installation checksums detect corrupted or mismatched downloads; release trust also depends on GitHub and the publisher. Verify published build provenance where your environment requires it.
