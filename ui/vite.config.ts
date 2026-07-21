import { defineConfig, loadEnv } from "vite";
import vue from "@vitejs/plugin-vue";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, ".", "");

  return {
    plugins: [vue()],
    server: {
      proxy: {
        "/api": env.VITE_API_PROXY_TARGET || "http://localhost:8080",
      },
    },
  };
});
