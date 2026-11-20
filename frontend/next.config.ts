import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Next generates frontend/AGENTS.md by default; the root AGENTS.md is the
  // single source of agent guidance.
  agentRules: false,
};

export default nextConfig;
