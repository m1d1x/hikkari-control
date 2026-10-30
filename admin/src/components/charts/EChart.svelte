<script lang="ts">
	import { onMount, untrack } from 'svelte';
	import { echarts, type EChartsOption, type EChartsInstance } from '$lib/utils/echarts';

	interface Props {
		option: EChartsOption;
		height?: string;
		class?: string;
	}

	let { option, height = '300px', class: className = '' }: Props = $props();

	let container: HTMLDivElement;
	let chart: EChartsInstance | null = null;
	let ready = false;

	// Only tracks `option` as dependency (chart/ready are untracked)
	// Fires only when parent passes a genuinely new option object
	$effect(() => {
		const opt = option;
		untrack(() => {
			if (chart && ready) {
				chart.setOption(opt);
			}
		});
	});

	onMount(() => {
		chart = echarts.init(container, 'nekko-admin');
		chart.setOption(option);
		ready = true;

		const ro = new ResizeObserver(() => chart?.resize());
		ro.observe(container);

		return () => {
			ready = false;
			ro.disconnect();
			chart?.dispose();
			chart = null;
		};
	});
</script>

<div bind:this={container} class="echart {className}" style="height: {height}"></div>

<style>
	.echart {
		width: 100%;
	}
</style>
