import unittest
from audit_public import inspect


class PublicAuditTest(unittest.TestCase):
    def test_private_paths_rejected(self):
        for path in [".local/config.json", "data/state.db", "private/key", "config.json", ".env.production", "notes.log", "admin-setup-code"]:
            with self.subTest(path=path):
                self.assertTrue(inspect(path, b""))

    def test_identifiers_detected_without_real_fixtures(self):
        # Construct synthetic shapes; no actual credential or personal number.
        values = [b"cli_" + b"a" * 16, b"ou_" + b"b" * 32,
                  b"+86" + b"1" + b"3" + b"0" * 9,
                  b"89" + b"0" * 18]
        for value in values:
            with self.subTest(kind=value[:2]):
                self.assertTrue(inspect("example.go", value))

    def test_public_placeholders_and_reserved_fictional_number(self):
        self.assertFalse(inspect("config.example.json", b'"app_id":"cli_REPLACE_ME"'))
        self.assertFalse(inspect("fixture_test.go", b'"+12025550100"'))


if __name__ == "__main__":
    unittest.main()
