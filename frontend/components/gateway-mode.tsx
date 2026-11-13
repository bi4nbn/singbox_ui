"use client"

import { useCallback, useEffect, useState } from "react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { useToast } from "@/hooks/use-toast"
import { Activity, Eye, HeartPulse, Play, RefreshCw, Square } from "lucide-react"

interface GatewayConfig {
  enabled: boolean
  interface: string
  lan_cidr: string
  tproxy_port: number
  mark: number
  table: number
  exclude_cidrs: string[]
  exclude_ports: number[]
  self_heal: boolean
  heal_interval: number
  masquerade: boolean
}

interface GatewayStatus {
  enabled: boolean
  interface: string
  auto_detect: boolean
  table_name: string
  nft_ok: boolean
  ip_rule_ok: boolean
  ip_route_ok: boolean
  healthy: boolean
  last_apply: string
  last_error: string
  heal_count: number
  nft_rules: string
  marker: string
  rule_table: number
  tproxy_port: number
  lan_cidr: string
  now: string
}

interface InterfaceInfo {
  name: string
  default: boolean
  addresses: string
}

const EMPTY_CFG: GatewayConfig = {
  enabled: false,
  interface: "",
  lan_cidr: "192.168.0.0/16",
  tproxy_port: 9888,
  mark: 1,
  table: 100,
  exclude_cidrs: ["10.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", "240.0.0.0/4"],
  exclude_ports: [53, 123],
  self_heal: true,
  heal_interval: 20,
  masquerade: true,
}

function TripleState({ ok, label }: { ok: boolean; label: string }) {
  return (
    <div className="flex items-center justify-between rounded-lg border px-3 py-2">
      <span className="text-sm text-muted-foreground">{label}</span>
      <Badge variant={ok ? "default" : "destructive"} className="text-[11px]">
        {ok ? "正常" : "缺失"}
      </Badge>
    </div>
  )
}

export function GatewayMode() {
  const { toast } = useToast()
  const [status, setStatus] = useState<GatewayStatus | null>(null)
  const [cfg, setCfg] = useState<GatewayConfig>(EMPTY_CFG)
  const [ifaces, setIfaces] = useState<InterfaceInfo[]>([])
  const [cidrText, setCidrText] = useState(EMPTY_CFG.exclude_cidrs.join(", "))
  const [portText, setPortText] = useState(EMPTY_CFG.exclude_ports.join(", "))
  const [busy, setBusy] = useState(false)
  const [rules, setRules] = useState("")
  const [rulesOpen, setRulesOpen] = useState(false)
  const [loadedOnce, setLoadedOnce] = useState(false)

  const load = useCallback(async () => {
    try {
      const [sRes, cRes, iRes] = await Promise.all([
        fetch("/api/gateway/status"),
        fetch("/api/gateway/config"),
        fetch("/api/gateway/interfaces"),
      ])
      if (sRes.ok) setStatus(await sRes.json())
      if (cRes.ok) {
        const c: GatewayConfig = await cRes.json()
        setCfg(c)
        if (!loadedOnce) {
          setCidrText((c.exclude_cidrs || []).join(", "))
          setPortText((c.exclude_ports || []).join(", "))
          setLoadedOnce(true)
        }
      }
      if (iRes.ok) {
        const d = await iRes.json()
        setIfaces(d.interfaces || [])
      }
    } catch {
      /* 网络抖动时保留上一轮状态 */
    }
  }, [loadedOnce])

  useEffect(() => {
    load()
    const timer = setInterval(load, 5000)
    return () => clearInterval(timer)
  }, [load])

  const buildPayload = (): GatewayConfig => ({
    ...cfg,
    exclude_cidrs: cidrText.split(/[,\s]+/).map((s) => s.trim()).filter(Boolean),
    exclude_ports: portText
      .split(/[,\s]+/)
      .map((s) => Number(s.trim()))
      .filter((n) => Number.isFinite(n) && n > 0),
  })

  const post = async (path: string, body?: unknown) => {
    setBusy(true)
    try {
      const res = await fetch(path, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: body ? JSON.stringify(body) : undefined,
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`)
      return data
    } finally {
      setBusy(false)
    }
  }

  const onApply = async () => {
    try {
      const data = await post("/api/gateway/apply", buildPayload())
      if (data.restart_required) {
        toast({
          title: "网关规则已应用",
          description: "已在默认实例配置中注入 tproxy 入站，请到「入站」页重启实例后生效。",
        })
      } else {
        toast({ title: "网关规则已应用", description: "nft 表、ip rule、ip route 均已就绪。" })
      }
      load()
    } catch (e) {
      toast({ title: "应用失败", description: String((e as Error).message), variant: "destructive" })
    }
  }

  const onStop = async () => {
    try {
      await post("/api/gateway/stop")
      toast({ title: "已停用网关模式", description: "独立表与 ip rule 已清理。" })
      load()
    } catch (e) {
      toast({ title: "停用失败", description: String((e as Error).message), variant: "destructive" })
    }
  }

  const onHeal = async () => {
    try {
      await post("/api/gateway/heal")
      toast({ title: "自愈已执行", description: "缺失的规则已重建。" })
      load()
    } catch (e) {
      toast({ title: "自愈失败", description: String((e as Error).message), variant: "destructive" })
    }
  }

  const onPreview = async () => {
    try {
      const res = await fetch("/api/gateway/rules")
      const data = await res.json()
      if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`)
      setRules(data.nft)
      setRulesOpen(true)
    } catch (e) {
      toast({ title: "预览失败", description: String((e as Error).message), variant: "destructive" })
    }
  }

  const healthy = status?.healthy ?? false

  return (
    <div className="space-y-6">
      <div className="grid gap-4 md:grid-cols-3">
        <Card>
          <CardHeader className="pb-3">
            <CardDescription>运行状态</CardDescription>
            <CardTitle className="flex items-center gap-2 text-2xl">
              <Activity className={`h-5 w-5 ${healthy ? "text-emerald-500" : "text-amber-500"}`} />
              {healthy ? "健康" : status?.enabled ? "异常" : "未启用"}
            </CardTitle>
          </CardHeader>
          <CardContent className="text-sm text-muted-foreground space-y-1">
            <div>出口接口：{status?.interface || "-"}{status?.auto_detect ? "（自动）" : "（指定）"}</div>
            <div>上次应用：{status?.last_apply || "-"}</div>
            <div>自愈次数：{status?.heal_count ?? 0}</div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="pb-3">
            <CardDescription>三要素</CardDescription>
            <CardTitle className="text-base">tproxy 生效的必要条件</CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            <TripleState ok={status?.nft_ok ?? false} label={`nft 表 ${status?.table_name || "ip smbox"}`} />
            <TripleState ok={status?.ip_rule_ok ?? false} label={`ip rule fwmark ${status?.marker || "0x1"} → ${status?.rule_table ?? 100}`} />
            <TripleState ok={status?.ip_route_ok ?? false} label="ip route local 0.0.0.0/0 table" />
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="pb-3">
            <CardDescription>参数</CardDescription>
            <CardTitle className="text-base">当前监听</CardTitle>
          </CardHeader>
          <CardContent className="text-sm text-muted-foreground space-y-1">
            <div>tproxy 端口：{status?.tproxy_port ?? cfg.tproxy_port}</div>
            <div>局域网网段：{status?.lan_cidr || cfg.lan_cidr}</div>
            <div>masquerade：{cfg.masquerade ? "开" : "关"}</div>
            {status?.last_error ? <div className="text-destructive break-all">最近错误：{status.last_error}</div> : null}
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>网关配置</CardTitle>
          <CardDescription>
            以独立表 ip smbox 部署 tproxy，不 flush ruleset；配合 ip rule 与 local 路由表让流量进入 sing-box。
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-4 md:grid-cols-2">
            <div className="space-y-1.5">
              <Label>出口接口</Label>
              <Select
                value={cfg.interface || "__auto__"}
                onValueChange={(v) => setCfg({ ...cfg, interface: v === "__auto__" ? "" : v })}
              >
                <SelectTrigger>
                  <SelectValue placeholder="自动探测" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__auto__">自动（默认路由接口）</SelectItem>
                  {ifaces.map((i) => (
                    <SelectItem key={i.name} value={i.name}>
                      {i.name}{i.default ? "（当前默认）" : ""}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-1.5">
              <Label>局域网网段（masquerade 源）</Label>
              <Input value={cfg.lan_cidr} onChange={(e) => setCfg({ ...cfg, lan_cidr: e.target.value })} />
            </div>

            <div className="space-y-1.5">
              <Label>tproxy 端口</Label>
              <Input
                type="number"
                value={cfg.tproxy_port}
                onChange={(e) => setCfg({ ...cfg, tproxy_port: Number(e.target.value) })}
              />
            </div>

            <div className="space-y-1.5">
              <Label>自愈间隔（秒）</Label>
              <Input
                type="number"
                value={cfg.heal_interval}
                onChange={(e) => setCfg({ ...cfg, heal_interval: Number(e.target.value) })}
              />
            </div>

            <div className="space-y-1.5">
              <Label>fwmark</Label>
              <Input
                type="number"
                value={cfg.mark}
                onChange={(e) => setCfg({ ...cfg, mark: Number(e.target.value) })}
              />
            </div>

            <div className="space-y-1.5">
              <Label>路由表号</Label>
              <Input
                type="number"
                value={cfg.table}
                onChange={(e) => setCfg({ ...cfg, table: Number(e.target.value) })}
              />
            </div>

            <div className="space-y-1.5 md:col-span-2">
              <Label>直连网段（不代理，逗号分隔）</Label>
              <Input value={cidrText} onChange={(e) => setCidrText(e.target.value)} />
            </div>

            <div className="space-y-1.5 md:col-span-2">
              <Label>直连端口（不代理，逗号分隔）</Label>
              <Input value={portText} onChange={(e) => setPortText(e.target.value)} />
            </div>
          </div>

          <div className="flex flex-wrap items-center gap-6 pt-1">
            <label className="flex items-center gap-2 text-sm">
              <Checkbox checked={cfg.self_heal} onCheckedChange={(v) => setCfg({ ...cfg, self_heal: !!v })} />
              启用自愈
            </label>
            <label className="flex items-center gap-2 text-sm">
              <Checkbox checked={cfg.masquerade} onCheckedChange={(v) => setCfg({ ...cfg, masquerade: !!v })} />
              出站 masquerade
            </label>
          </div>

          <div className="flex flex-wrap gap-2 pt-2">
            <Button onClick={onApply} disabled={busy}>
              <Play className="h-4 w-4 mr-2" />
              应用并启用
            </Button>
            <Button variant="outline" onClick={onStop} disabled={busy}>
              <Square className="h-4 w-4 mr-2" />
              停用并清理
            </Button>
            <Button variant="outline" onClick={onHeal} disabled={busy}>
              <HeartPulse className="h-4 w-4 mr-2" />
              立即自愈
            </Button>
            <Button variant="outline" onClick={onPreview} disabled={busy}>
              <Eye className="h-4 w-4 mr-2" />
              预览 nft
            </Button>
            <Button variant="ghost" onClick={load} disabled={busy}>
              <RefreshCw className="h-4 w-4 mr-2" />
              刷新
            </Button>
          </div>
        </CardContent>
      </Card>

      <Dialog open={rulesOpen} onOpenChange={setRulesOpen}>
        <DialogContent className="max-w-3xl">
          <DialogHeader>
            <DialogTitle>将加载的 nft 规则</DialogTitle>
            <DialogDescription>独立表 ip smbox，仅删除/重建自身表，不动 firewalld 等其他表。</DialogDescription>
          </DialogHeader>
          <pre className="max-h-[60vh] overflow-auto rounded-lg bg-zinc-950 text-zinc-100 p-4 text-xs leading-relaxed">
            {rules}
          </pre>
        </DialogContent>
      </Dialog>
    </div>
  )
}
