import * as echarts from 'echarts/core';
import { LineChart, BarChart, HeatmapChart, FunnelChart, GaugeChart, ScatterChart } from 'echarts/charts';
import type {
	LineSeriesOption,
	BarSeriesOption,
	HeatmapSeriesOption,
	FunnelSeriesOption,
	GaugeSeriesOption,
	ScatterSeriesOption,
} from 'echarts/charts';
import {
	GridComponent,
	TooltipComponent,
	LegendComponent,
	DataZoomComponent,
	TitleComponent,
	VisualMapComponent,
	ToolboxComponent,
	MarkLineComponent,
} from 'echarts/components';
import type {
	GridComponentOption,
	TooltipComponentOption,
	LegendComponentOption,
	DataZoomComponentOption,
	TitleComponentOption,
	VisualMapComponentOption,
	ToolboxComponentOption,
} from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';
import { chartColors } from './colors';

echarts.use([
	LineChart,
	BarChart,
	HeatmapChart,
	FunnelChart,
	GaugeChart,
	ScatterChart,
	GridComponent,
	TooltipComponent,
	LegendComponent,
	DataZoomComponent,
	TitleComponent,
	VisualMapComponent,
	ToolboxComponent,
	MarkLineComponent,
	CanvasRenderer,
]);

// All colors must be hex/rgb — ECharts canvas doesn't support oklch
echarts.registerTheme('hikkari-admin', {
	backgroundColor: 'transparent',
	textStyle: {
		color: '#989898',
		fontFamily: "'Inter', sans-serif",
		fontSize: 12,
	},
	color: chartColors,
	categoryAxis: {
		axisLine: { lineStyle: { color: '#3a3a3a' } },
		axisTick: { lineStyle: { color: '#3a3a3a' } },
		axisLabel: { color: '#7e7e7e' },
		splitLine: { lineStyle: { color: '#2c2c2c' } },
	},
	valueAxis: {
		axisLine: { show: false },
		axisTick: { show: false },
		axisLabel: { color: '#7e7e7e' },
		splitLine: { lineStyle: { color: '#2c2c2c' } },
	},
	tooltip: {
		backgroundColor: '#262626',
		borderColor: '#3a3a3a',
		borderWidth: 1,
		textStyle: {
			color: '#ededed',
			fontSize: 12,
		},
	},
	legend: {
		textStyle: { color: '#989898' },
	},
});

export { echarts };
export type EChartsOption = echarts.ComposeOption<
	| LineSeriesOption
	| BarSeriesOption
	| HeatmapSeriesOption
	| FunnelSeriesOption
	| GaugeSeriesOption
	| ScatterSeriesOption
	| GridComponentOption
	| TooltipComponentOption
	| LegendComponentOption
	| DataZoomComponentOption
	| TitleComponentOption
	| VisualMapComponentOption
	| ToolboxComponentOption
>;
export type EChartsInstance = echarts.ECharts;
