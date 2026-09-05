import { Component } from 'svelte';
import { FallbackMetadata } from '../types';
declare const FallbackCard: Component<{ metadata: FallbackMetadata; reason?: string }>;
export default FallbackCard;
