import { Inbox } from "../src/components/inbox";
import { PingStatus } from "./ping-status";

export default function Home() {
  return (
    <>
      <main>
        <h1>LemmyScheduler</h1>
        <p className="banner" role="status">
          Publishing isn&apos;t enabled yet: scheduled posts will not be sent to Lemmy.
        </p>
        <Inbox />
      </main>
      <footer className="site-footer">
        <PingStatus />
      </footer>
    </>
  );
}
