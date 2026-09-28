from pathlib import Path
import tempfile
import unittest

from testnet import read_secrets, require_intent_scope


class SecretInjectionTests(unittest.TestCase):
    def test_execution_scope_cannot_be_empty_missing_or_repeated(self):
        identifier = "0x" + "12" * 32
        require_intent_scope(["run", "-intent", identifier])
        require_intent_scope(["run", "--intent=" + identifier])
        for arguments in (["run"], ["run", "-intent"], ["run", "-intent="],
                          ["run", "-intent", "bad"], ["run", "-intent", identifier, "--intent=" + identifier]):
            with self.subTest(arguments=arguments), self.assertRaises(ValueError):
                require_intent_scope(arguments)

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
