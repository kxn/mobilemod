# Security

Do not put app credentials, personal phone numbers, SIM identifiers, message
contents, private configuration, databases or diagnostic captures in this public
repository, issues, pull requests or workflow logs. This applies equally to test
accounts and test SIMs. Automated tests use synthetic values and local simulators;
CI and releases require no Feishu credentials.

Keep production configuration outside the checkout or in an ignored private
directory, restrict OS file permissions, and set a nonempty controlling-user ACL.
Forwarded SMS and local database records contain private information. See README
and the deployment guide for trust boundaries and delivery limitations.

Use GitHub private vulnerability reporting for security findings. Supply a minimal
reproducer with invented identifiers and messages. Do not include real credentials
or private modem dumps. Public CI runs Gitleaks against all history and an additional
public-file audit; release packaging uses an explicit allowlist.
