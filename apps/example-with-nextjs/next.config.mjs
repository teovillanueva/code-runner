/** @type {import('next').NextConfig} */
const config = {
  reactStrictMode: true,
  // The SDKs are workspace packages built by tsup; transpiling them lets Next
  // consume their ESM output (and pick up rebuilds) without interop surprises.
  transpilePackages: [
    "@teovilla/code-runner-react",
    "@teovilla/code-runner-sdk-node",
    "@teovilla/code-runner-contract",
  ],
};

export default config;
