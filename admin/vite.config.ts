import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [sveltekit()],
	server: {
		port: Number(process.env.ADMIN_DEV_PORT ?? 5174),
		proxy: {
			'/api': process.env.ADMIN_DEV_HUB ?? 'http://127.0.0.1:9445',
		},
	},
});
