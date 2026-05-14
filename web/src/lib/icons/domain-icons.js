import {
  IconAlertTriangle,
  IconCircleCheck,
  IconClipboardList,
  IconCopy,
  IconFile,
  IconFileText,
  IconFileTypePdf,
  IconFlower,
  IconInbox,
  IconJson,
  IconMusic,
  IconPackage,
  IconPhoto,
  IconQuestionMark,
  IconRocket,
  IconRotateClockwise,
  IconServer,
  IconShieldLock,
  IconSignature,
  IconTarget,
  IconVideo
} from '@tabler/icons-svelte';

export const ServiceIcon = IconServer;
export const ArtifactIcon = IconPackage;
export const EnvironmentIcon = IconTarget;
export const DeploymentIcon = IconRocket;
export const RollbackIcon = IconRotateClockwise;
export const BlossomIcon = IconFlower;
export const ProtectedIcon = IconShieldLock;
export const WarningIcon = IconAlertTriangle;
export const SuccessIcon = IconCircleCheck;
export const UnknownIcon = IconQuestionMark;
export const CopyIcon = IconCopy;
export const SbomIcon = IconClipboardList;
export const SignatureIcon = IconSignature;
export const EmptyIcon = IconInbox;

export const ImageFileIcon = IconPhoto;
export const VideoFileIcon = IconVideo;
export const AudioFileIcon = IconMusic;
export const JsonFileIcon = IconJson;
export const TextFileIcon = IconFileText;
export const PdfFileIcon = IconFileTypePdf;
export const GenericFileIcon = IconFile;

export function blossomContentTypeIcon(contentType) {
  const normalized = (contentType || '').toLowerCase();

  if (normalized.startsWith('image/')) return ImageFileIcon;
  if (normalized.startsWith('video/')) return VideoFileIcon;
  if (normalized.startsWith('audio/')) return AudioFileIcon;
  if (normalized.includes('json')) return JsonFileIcon;
  if (normalized.startsWith('text/') || normalized.includes('text')) return TextFileIcon;
  if (normalized.includes('pdf')) return PdfFileIcon;

  return GenericFileIcon;
}
