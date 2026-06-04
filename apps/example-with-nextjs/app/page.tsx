import { soketiPublicConfig } from "@/app/lib/public-config";
import { Playground } from "./_components/playground";

// Server Component: read the PUBLIC soketi config (app key + host, no secret)
// and hand it to the client playground.
export default function Page() {
  return <Playground soketi={soketiPublicConfig()} />;
}
