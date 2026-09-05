import { Component } from 'svelte';
import { NostrWidgetEvent, WidgetTheme } from '../types';
declare const WidgetRenderer: Component<{ event: NostrWidgetEvent; theme?: Partial<WidgetTheme> }>;
export default WidgetRenderer;
