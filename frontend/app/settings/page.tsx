import Link from "next/link";

import { AccountSettings } from "../../src/components/account-settings";

export default function SettingsPage() {
  return (
    <main>
      <Link href="/">← Inbox</Link>
      <h1>Settings</h1>
      <AccountSettings />
    </main>
  );
}
