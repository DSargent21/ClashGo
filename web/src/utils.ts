export const formatUptime = (ns: number) => {
  if (!ns) return '0h 0m';
  const seconds = Math.floor(ns / 1e9);
  const hrs = Math.floor(seconds / 3600);
  const mins = Math.floor((seconds % 3600) / 60);
  return `${hrs}h ${mins}m`;
};

export const cleanLogMessage = (msg: string) => {
  return msg.replace(/\x1b\[[0-9;]*m/g, '').trim();
};
