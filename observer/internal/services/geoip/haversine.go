package geoip

import (
	"math"
)

const earthRadiusKm = 6371.0

// toRadians конвертирует градусы в радианы
func toRadians(deg float64) float64 {
	return deg * math.Pi / 180.0
}

// HaversineDistance вычисляет расстояние между двумя точками на Земле (в км)
// используя формулу Haversine
func HaversineDistance(lat1, lon1, lat2, lon2 float64) float64 {
	dLat := toRadians(lat2 - lat1)
	dLon := toRadians(lon2 - lon1)

	lat1Rad := toRadians(lat1)
	lat2Rad := toRadians(lat2)

	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1Rad)*math.Cos(lat2Rad)*
			math.Sin(dLon/2)*math.Sin(dLon/2)

	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	return earthRadiusKm * c
}
