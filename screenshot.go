package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image/png"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultScreenshotTimeout = 10 * time.Second
	screenshotPollInterval   = 25 * time.Millisecond
)

var pngEndMarker = [12]byte{0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82}

type screenshotEngineResult struct {
	Tick    float64 `json:"tick"`
	Surface string  `json:"surface"`
	Player  string  `json:"player"`
}

func validateScreenshotInput(input ScreenshotInput) error {
	if strings.TrimSpace(input.Player) == "" {
		return errors.New("player must not be blank")
	}
	if strings.TrimSpace(input.Surface) == "" {
		return errors.New("surface must not be blank")
	}
	if input.Position == nil {
		return errors.New("position is required")
	}
	if !isFiniteFloat(input.Position.X) || !isFiniteFloat(input.Position.Y) {
		return errors.New("position x and y must be finite")
	}
	maxDimension := 4096
	if input.AntiAlias {
		maxDimension = 2048
	}
	if input.Width < 1 || input.Width > maxDimension {
		return fmt.Errorf("width must be between 1 and %d", maxDimension)
	}
	if input.Height < 1 || input.Height > maxDimension {
		return fmt.Errorf("height must be between 1 and %d", maxDimension)
	}
	if !isFiniteFloat(input.Zoom) || input.Zoom <= 0 {
		return errors.New("zoom must be finite and greater than zero")
	}
	if input.Daytime != nil && (!isFiniteFloat(*input.Daytime) || *input.Daytime < 0 || *input.Daytime > 1) {
		return errors.New("daytime must be finite and between 0 and 1")
	}
	if input.WaterTick != nil && *input.WaterTick < 0 {
		return errors.New("waterTick must be nonnegative")
	}
	return nil
}

func isFiniteFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func generatedScreenshotPath() (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate screenshot path: %w", err)
	}
	name := fmt.Sprintf("screenshot-%d-%s.png", time.Now().UTC().UnixNano(), hex.EncodeToString(random[:]))
	return path.Join("factorio-ai-player", name), nil
}

func screenshotRelativePath(r *Resolver) (string, error) {
	generator := generatedScreenshotPath
	if r.screenshotPath != nil {
		generator = r.screenshotPath
	}
	relative, err := generator()
	if err != nil {
		return "", err
	}
	relative = path.Clean(filepath.ToSlash(relative))
	if relative == "factorio-ai-player" || !strings.HasPrefix(relative, "factorio-ai-player/") || path.Ext(relative) != ".png" {
		return "", errors.New("generated screenshot path must be a PNG under factorio-ai-player/")
	}
	return relative, nil
}

func screenshotLua(input ScreenshotInput, relativePath string) string {
	var optional strings.Builder
	if input.Daytime != nil {
		fmt.Fprintf(&optional, ",daytime=%s", formatFloat(*input.Daytime))
	}
	if input.WaterTick != nil {
		fmt.Fprintf(&optional, ",water_tick=%d", *input.WaterTick)
	}
	return luaFail + fmt.Sprintf(`local p=game.get_player(%s)
if not p or not p.connected then fail("player is not connected"); return end
local s=game.surfaces[%s]
if not s then fail("surface not found"); return end
game.take_screenshot{path=%s,surface=s,position={x=%s,y=%s},resolution={x=%d,y=%d},zoom=%s,show_gui=%t,show_entity_info=%t,anti_alias=%t,hide_clouds=%t,hide_fog=%t,force_render=true,player=p,by_player=p%s}
game.set_wait_for_screenshots_to_finish()
rcon.print(helpers.table_to_json({tick=game.tick,surface=s.name,player=p.name}))`,
		luaString(strings.TrimSpace(input.Player)), luaString(strings.TrimSpace(input.Surface)), luaString(relativePath),
		luaCoordinate(input.Position.X), luaCoordinate(input.Position.Y), input.Width, input.Height, formatFloat(input.Zoom),
		input.ShowGui, input.ShowEntityInfo, input.AntiAlias, input.HideClouds, input.HideFog, optional.String())
}

func screenshotMutation(ctx context.Context, r *Resolver, input ScreenshotInput) (*ScreenshotResult, error) {
	if err := validateScreenshotInput(input); err != nil {
		return nil, err
	}
	relativePath, err := screenshotRelativePath(r)
	if err != nil {
		return nil, err
	}
	root := r.screenshotDir
	if root == "" {
		root = configuredScreenshotDir()
	}
	localPath := filepath.Join(root, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return nil, fmt.Errorf("prepare screenshot output directory: %w", err)
	}
	if _, err := os.Lstat(localPath); err == nil {
		return nil, errors.New("generated screenshot path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("check screenshot output path: %w", err)
	}

	timeout := r.screenshotTimeout
	if timeout <= 0 || timeout > defaultScreenshotTimeout {
		timeout = defaultScreenshotTimeout
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	response, _, err := executeLuaContext(bounded, r.addr, r.password, screenshotLua(input, relativePath))
	if err != nil {
		return nil, fmt.Errorf("request native screenshot: %w", err)
	}
	engine, err := factoryDecode[screenshotEngineResult](response)
	if err != nil {
		return nil, err
	}
	bytes, err := waitForScreenshot(bounded, localPath, input.Width, input.Height)
	if err != nil {
		return nil, fmt.Errorf("verify screenshot %q: %w", relativePath, err)
	}
	result := &ScreenshotResult{
		Tick: engine.Tick, Surface: engine.Surface, Player: engine.Player, Path: relativePath,
		Width: input.Width, Height: input.Height, Bytes: bytes,
	}
	announceMutation(r, input.Message, fmt.Sprintf("screenshot captured by %s: %s (%dx%d, %d bytes)", result.Player, result.Path, result.Width, result.Height, result.Bytes))
	return result, nil
}

func waitForScreenshot(ctx context.Context, filename string, width, height int) (int, error) {
	ticker := time.NewTicker(screenshotPollInterval)
	defer ticker.Stop()
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return 0, fmt.Errorf("%w: %v", err, lastErr)
			}
			return 0, err
		}
		bytes, pending, err := inspectScreenshot(filename, width, height)
		if err == nil {
			return bytes, nil
		}
		lastErr = err
		if !pending {
			return 0, err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return 0, fmt.Errorf("%w: %v", ctx.Err(), lastErr)
			}
			return 0, ctx.Err()
		case <-ticker.C:
		}
	}
}

func inspectScreenshot(filename string, width, height int) (int, bool, error) {
	file, err := os.Open(filename)
	if errors.Is(err, os.ErrNotExist) {
		return 0, true, errors.New("PNG has not appeared")
	}
	if err != nil {
		return 0, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, false, err
	}
	if !info.Mode().IsRegular() {
		return 0, false, errors.New("output is not a regular file")
	}
	config, err := png.DecodeConfig(file)
	if err != nil {
		return 0, true, fmt.Errorf("decode PNG configuration: %w", err)
	}
	if config.Width != width || config.Height != height {
		return 0, false, fmt.Errorf("PNG dimensions are %dx%d, want %dx%d", config.Width, config.Height, width, height)
	}
	if info.Size() < int64(len(pngEndMarker)) {
		return 0, true, errors.New("PNG is incomplete")
	}
	var trailer [len(pngEndMarker)]byte
	if _, err := file.ReadAt(trailer[:], info.Size()-int64(len(trailer))); err != nil {
		return 0, true, fmt.Errorf("read PNG end marker: %w", err)
	}
	if trailer != pngEndMarker {
		return 0, true, errors.New("PNG is incomplete")
	}
	if info.Size() > math.MaxInt32 {
		return 0, false, errors.New("PNG is too large to report")
	}
	return int(info.Size()), false, nil
}
