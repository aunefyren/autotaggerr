package utilities

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

func PrintASCII() {
	fmt.Println(``)
	fmt.Println(`A U T O T A G G E R R`)
	fmt.Println(``)
	return
}

func ValidatePasswordFormat(password string) (bool, string, error) {
	requirements := "Password must have a minimum of eight characters, at least one uppercase letter, one lowercase letter and one number."

	if len(password) < 8 {
		return false, requirements, nil
	}

	match, err := regexp.Match(`[A-ZÆØÅ]{1,20}`, []byte(password))
	if err != nil {
		return false, requirements, err
	} else if !match {
		return false, requirements, nil
	}

	match, err = regexp.Match(`[a-zæøå]{1,20}`, []byte(password))
	if err != nil {
		return false, requirements, err
	} else if !match {
		return false, requirements, nil
	}

	match, err = regexp.Match(`[0-9]{1,20}`, []byte(password))
	if err != nil {
		return false, requirements, err
	} else if !match {
		return false, requirements, nil
	}

	return true, requirements, nil
}

func FindNextSunday(poinInTime time.Time) (time.Time, error) {

	sundayDate := time.Time{}

	// Find sunday
	if poinInTime.Weekday() == 0 {
		sundayDate = poinInTime
	} else {
		nextDate := poinInTime

		for i := 0; i < 8; i++ {
			nextDate = nextDate.AddDate(0, 0, +1)
			if nextDate.Weekday() == 0 {
				sundayDate = nextDate
				break
			}
		}

	}

	if sundayDate.Weekday() == 0 {
		return SetClockToMaximum(sundayDate), nil
	}

	return time.Time{}, errors.New("Failed to find next sunday for date.")
}

func FindEarlierMonday(pointInTime time.Time) (time.Time, error) {

	mondayDate := time.Time{}

	// Find monday
	if pointInTime.Weekday() == 1 {
		mondayDate = pointInTime
	} else {
		previousDate := pointInTime

		for i := 0; i < 8; i++ {
			previousDate = previousDate.AddDate(0, 0, -1)
			if previousDate.Weekday() == 1 {
				mondayDate = previousDate
				break
			}
		}

	}

	if mondayDate.Weekday() == 1 {
		return SetClockToMinimum(mondayDate), nil
	}

	return time.Time{}, errors.New("Failed to find earlier monday for date.")
}

func FindEarlierSunday(pointInTime time.Time) (time.Time, error) {

	sundayDate := time.Time{}

	// Find monday
	if pointInTime.Weekday() == 0 {
		sundayDate = pointInTime
	} else {
		previousDate := pointInTime

		for i := 0; i < 8; i++ {
			previousDate = previousDate.AddDate(0, 0, -1)
			if previousDate.Weekday() == 0 {
				sundayDate = previousDate
				break
			}
		}

	}

	if sundayDate.Weekday() == 0 {
		return sundayDate, nil
	}

	return time.Time{}, errors.New("Failed to find earlier Sunday for date.")
}

func RemoveIntFromArray(originalArray []int, intToRemove int) []int {

	newArray := []int{}

	for _, intNumber := range originalArray {
		if intNumber != intToRemove {
			newArray = append(newArray, intNumber)
		}
	}

	return newArray

}

func SetClockToMinimum(pointInTime time.Time) (newPointInTime time.Time) {
	newPointInTime = SetClockToTime(pointInTime, 0, 0, 0, 0)
	return
}

func SetClockToMaximum(pointInTime time.Time) (newPointInTime time.Time) {
	newPointInTime = SetClockToTime(pointInTime, 23, 59, 59, 59)
	return
}

func SetClockToTime(pointInTime time.Time, hours int, minutes int, seconds int, nanoSeconds int) (newPointInTime time.Time) {
	newPointInTime = time.Date(pointInTime.Year(), pointInTime.Month(), pointInTime.Day(), hours, minutes, seconds, nanoSeconds, time.Now().Location())
	return
}

func TimeToMySQLTimestamp(pointInTime time.Time) (timeString string) {
	timeString = ""
	timeString = IntToPaddedString(pointInTime.Year()) + "-" + IntToPaddedString(int(pointInTime.Month())) + "-" + IntToPaddedString(pointInTime.Day()) + " " + IntToPaddedString(pointInTime.Hour()) + ":" + IntToPaddedString(pointInTime.Minute()) + ":" + IntToPaddedString(pointInTime.Second()) + ".000"
	return
}

func IntToPaddedString(number int) (paddedNumber string) {
	paddedNumber = ""
	if number > 9 {
		return strconv.Itoa(number)
	} else {
		paddedNumber = "0" + strconv.Itoa(number)
	}
	return
}
