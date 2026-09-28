from pathlib import Path
import tempfile
import unittest

from testnet import read_secrets


class SecretInjectionTests(unittest.TestCase):
    def test_values_are_literal_and_never_sourced(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "testnet.env"
            path.write_text("LIFI_API_KEY=$(touch-do-not-run)\nPOLYMER_API_KEY=test-proof\nSOLVER_PRIVATE_KEY=test-key\n")
            path.chmod(0o600)
            self.assertEqual(read_secrets(path)["LIFI_API_KEY"], "$(touch-do-not-run)")

    def test_rejects_shared_permissions_and_symlinks(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "testnet.env"
            path.write_text("LIFI_API_KEY=test\nPOLYMER_API_KEY=test\nSOLVER_PRIVATE_KEY=test\n")
            path.chmod(0o644)
            with self.assertRaises(ValueError):
                read_secrets(path)
            path.chmod(0o600)
            link = Path(directory) / "link"
            link.symlink_to(path)
            with self.assertRaises(OSError):
                read_secrets(link)

    def test_rejects_duplicate_or_incomplete_keys(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "testnet.env"
            path.write_text("LIFI_API_KEY=first\nLIFI_API_KEY=second\n")
            path.chmod(0o600)
            with self.assertRaises(ValueError):
                read_secrets(path)


if __name__ == "__main__":
    unittest.main()
