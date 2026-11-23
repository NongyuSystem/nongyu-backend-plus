package classroom

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"nongyu-backend-plus/internal/apperr"
)

// RegisterPublic 挂载读者侧（需登录鉴权，由全局会话中间件负责）。
//
// 2026-11-23 起查教室地址/导航需要登录：数据含校内楼栋坐标与进入路线，
// 按用户身份提供；本包不重复鉴权，只负责路由与业务。
func RegisterPublic(r gin.IRouter, svc *Service) {
	r.GET("/classrooms/lookup", lookupHandler(svc))
	r.GET("/classrooms/buildings", listBuildingsHandler(svc))
	r.GET("/classrooms/buildings/:id", getBuildingHandler(svc))
}

// RegisterAdmin 挂载管理端（楼栋与路线的 CRUD）。
//
// 装配层负责挂 admin 会话 + 角色门槛；本包不重复鉴权。
func RegisterAdmin(r gin.IRouter, svc *Service) {
	r.GET("/admin/classrooms/buildings", adminListHandler(svc))
	r.POST("/admin/classrooms/buildings", adminCreateHandler(svc))
	r.GET("/admin/classrooms/buildings/:id", adminGetHandler(svc))
	r.PATCH("/admin/classrooms/buildings/:id", adminUpdateHandler(svc))
	r.DELETE("/admin/classrooms/buildings/:id", adminDeleteHandler(svc))

	r.POST("/admin/classrooms/buildings/:id/routes", adminCreateRouteHandler(svc))
	r.PATCH("/admin/classrooms/routes/:routeId", adminUpdateRouteHandler(svc))
	r.DELETE("/admin/classrooms/routes/:routeId", adminDeleteRouteHandler(svc))
}

// ---------- 响应体 ----------

type buildingView struct {
	ID          int64       `json:"id"`
	Key         string      `json:"key"`
	No          string      `json:"no"`
	Zone        string      `json:"zone,omitempty"`
	DisplayName string      `json:"displayName"`
	Aliases     []string    `json:"aliases,omitempty"`
	Campus      string      `json:"campus,omitempty"`
	Lat         *float64    `json:"lat,omitempty"`
	Lng         *float64    `json:"lng,omitempty"`
	AddressText string      `json:"addressText,omitempty"`
	Note        string      `json:"note,omitempty"`
	Enabled     bool        `json:"enabled"`
	Routes      []routeView `json:"routes,omitempty"`
}

type routeView struct {
	ID          int64       `json:"id"`
	Title       string      `json:"title"`
	Description string      `json:"description,omitempty"`
	StartLabel  string      `json:"startLabel,omitempty"`
	Recommended bool        `json:"recommended"`
	SortOrder   int         `json:"sortOrder"`
	Enabled     bool        `json:"enabled"`
	Points      []pointView `json:"points"`
}

type pointView struct {
	Seq   int     `json:"seq"`
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
	Label string  `json:"label,omitempty"`
}

func toBuildingView(b *Building) buildingView {
	v := buildingView{
		ID: b.ID, Key: b.Key, No: b.No, Zone: b.Zone,
		DisplayName: b.DisplayName, Aliases: b.Aliases, Campus: b.Campus,
		Lat: b.Lat, Lng: b.Lng, AddressText: b.AddressText, Note: b.Note,
		Enabled: b.Enabled,
	}
	if len(b.Routes) > 0 {
		v.Routes = make([]routeView, 0, len(b.Routes))
		for _, rt := range b.Routes {
			v.Routes = append(v.Routes, toRouteView(rt))
		}
	}
	return v
}

func toRouteView(r *Route) routeView {
	v := routeView{
		ID: r.ID, Title: r.Title, Description: r.Description,
		StartLabel: r.StartLabel, Recommended: r.Recommended,
		SortOrder: r.SortOrder, Enabled: r.Enabled,
		Points: make([]pointView, 0, len(r.Points)),
	}
	for _, p := range r.Points {
		v.Points = append(v.Points, pointView{Seq: p.Seq, Lat: p.Lat, Lng: p.Lng, Label: p.Label})
	}
	return v
}

// ---------- 读者侧 ----------

// lookupHandler 按教室名查楼栋。
//
// 返回结构里同时带上 Input / BuildingKey / RoomNo，
// 让前端能显示「10-B213 → 十教B 栋」这种「我被归并到哪」的反馈。
func lookupHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := c.Query("room")
		if name == "" {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "缺少 room 参数"))
			return
		}
		res, err := svc.Lookup(c.Request.Context(), name)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		out := gin.H{
			"input":       res.Input,
			"buildingKey": res.BuildingKey,
			"roomNo":      res.RoomNo,
			"found":       res.Building != nil,
		}
		if res.Building != nil {
			out["building"] = toBuildingView(res.Building)
		} else if res.BuildingKey == "" {
			// 明确区分「教室名无法识别」与「楼栋未录入」。
			out["reason"] = "unrecognized"
		} else {
			out["reason"] = "building_not_configured"
		}
		c.JSON(http.StatusOK, out)
	}
}

func listBuildingsHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		list, err := svc.ListBuildings(c.Request.Context(), c.Query("campus"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		out := make([]buildingView, 0, len(list))
		for _, b := range list {
			out = append(out, toBuildingView(b))
		}
		c.JSON(http.StatusOK, gin.H{"items": out})
	}
}

func getBuildingHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := parseID(c.Param("id"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		b, err := svc.GetBuilding(c.Request.Context(), id)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": toBuildingView(b)})
	}
}

// ---------- 管理端：楼栋 ----------

type buildingRequest struct {
	BuildingNo  string   `json:"buildingNo"`
	Zone        string   `json:"zone"`
	DisplayName string   `json:"displayName"`
	Aliases     []string `json:"aliases"`
	Campus      string   `json:"campus"`
	Lat         *float64 `json:"lat"`
	Lng         *float64 `json:"lng"`
	AddressText string   `json:"addressText"`
	Note        string   `json:"note"`
	Enabled     *bool    `json:"enabled"`
}

func (in buildingRequest) toInput() BuildingInput {
	return BuildingInput{
		BuildingNo: in.BuildingNo, Zone: in.Zone, DisplayName: in.DisplayName,
		Aliases: in.Aliases, Campus: in.Campus, Lat: in.Lat, Lng: in.Lng,
		AddressText: in.AddressText, Note: in.Note, Enabled: in.Enabled,
	}
}

func adminListHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		list, err := svc.AdminListBuildings(c.Request.Context(), c.Query("campus"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		out := make([]buildingView, 0, len(list))
		for _, b := range list {
			out = append(out, toBuildingView(b))
		}
		c.JSON(http.StatusOK, gin.H{"items": out})
	}
}

func adminCreateHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in buildingRequest
		if err := c.ShouldBindJSON(&in); err != nil {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "请求体非法"))
			return
		}
		b, err := svc.CreateBuilding(c.Request.Context(), in.toInput())
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": toBuildingView(b)})
	}
}

func adminGetHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := parseID(c.Param("id"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		b, err := svc.AdminGetBuilding(c.Request.Context(), id)
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": toBuildingView(b)})
	}
}

func adminUpdateHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := parseID(c.Param("id"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		var in buildingRequest
		if err := c.ShouldBindJSON(&in); err != nil {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "请求体非法"))
			return
		}
		b, err := svc.UpdateBuilding(c.Request.Context(), id, in.toInput())
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": toBuildingView(b)})
	}
}

func adminDeleteHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := parseID(c.Param("id"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		if err := svc.DeleteBuilding(c.Request.Context(), id); err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

// ---------- 管理端：路线 ----------

type routeRequest struct {
	Title       string         `json:"title"`
	Description string         `json:"description"`
	StartLabel  string         `json:"startLabel"`
	Recommended bool           `json:"recommended"`
	SortOrder   int            `json:"sortOrder"`
	Enabled     *bool          `json:"enabled"`
	Points      []pointRequest `json:"points"`
}

type pointRequest struct {
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
	Label string  `json:"label"`
}

func (in routeRequest) toInput() RouteInput {
	pts := make([]RoutePointInput, 0, len(in.Points))
	for _, p := range in.Points {
		pts = append(pts, RoutePointInput{Lat: p.Lat, Lng: p.Lng, Label: p.Label})
	}
	return RouteInput{
		Title: in.Title, Description: in.Description, StartLabel: in.StartLabel,
		Recommended: in.Recommended, SortOrder: in.SortOrder, Enabled: in.Enabled,
		Points: pts,
	}
}

func adminCreateRouteHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := parseID(c.Param("id"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		var in routeRequest
		if err := c.ShouldBindJSON(&in); err != nil {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "请求体非法"))
			return
		}
		rt, err := svc.CreateRoute(c.Request.Context(), id, in.toInput())
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": toRouteView(rt)})
	}
}

func adminUpdateRouteHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := parseID(c.Param("routeId"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		var in routeRequest
		if err := c.ShouldBindJSON(&in); err != nil {
			apperr.WriteGin(c, apperr.Errorf(apperr.CodeValidationFailed, "请求体非法"))
			return
		}
		rt, err := svc.UpdateRoute(c.Request.Context(), id, in.toInput())
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": toRouteView(rt)})
	}
}

func adminDeleteRouteHandler(svc *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := parseID(c.Param("routeId"))
		if err != nil {
			apperr.WriteGin(c, err)
			return
		}
		if err := svc.DeleteRoute(c.Request.Context(), id); err != nil {
			apperr.WriteGin(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

func parseID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, apperr.Errorf(apperr.CodeValidationFailed, "id 非法")
	}
	return id, nil
}
